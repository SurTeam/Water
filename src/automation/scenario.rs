use std::path::Path;

use serde::{Deserialize, Serialize};
use thiserror::Error;

use crate::app::model::{ModelSnapshot, PaneTreeDump};
use crate::command::{
    AppCommand, CommandDispatcher, OperationResult, OperationSnapshot, OperationStatus,
};
use crate::control::ControlClient;
use crate::event::AppEvent;
use crate::ids::{OperationId, TerminalId};
use crate::surface::SurfaceKind;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct Scenario {
    pub name: String,
    pub steps: Vec<ScenarioStep>,
}

impl Scenario {
    pub fn from_json(text: &str) -> Result<Self, ScenarioError> {
        serde_json::from_str(text).map_err(|error| ScenarioError::InvalidScenario {
            message: error.to_string(),
        })
    }

    pub fn from_path(path: impl AsRef<Path>) -> Result<Self, ScenarioError> {
        let path = path.as_ref();
        let text = std::fs::read_to_string(path).map_err(|error| ScenarioError::Io {
            path: path.display().to_string(),
            message: error.to_string(),
        })?;
        Self::from_json(&text)
    }
}

#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct ScenarioStep {
    pub command: Option<AppCommand>,
    pub assert: Option<ScenarioAssertion>,
    pub wait: Option<WaitPrimitive>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum ScenarioAssertion {
    PaneCount { value: usize },
    TabCount { value: usize },
    ActiveSurfaceKind { kind: SurfaceKind },
    StateRevisionAtLeast { value: u64 },
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum WaitPrimitive {
    OperationComplete {
        operation_id: Option<OperationId>,
    },
    Event {
        event_type: String,
    },
    StateRevisionAtLeast {
        value: u64,
    },
    AppIdle,
    TerminalContains {
        terminal_id: Option<TerminalId>,
        text: String,
        #[serde(default = "default_wait_timeout_ms")]
        timeout_ms: u64,
    },
    ProcessExit {
        terminal_id: Option<TerminalId>,
        #[serde(default = "default_wait_timeout_ms")]
        timeout_ms: u64,
    },
}

fn default_wait_timeout_ms() -> u64 {
    5_000
}

#[derive(Debug, Error)]
pub enum ScenarioError {
    #[error("scenario I/O failed for {path}: {message}")]
    Io { path: String, message: String },
    #[error("invalid scenario: {message}")]
    InvalidScenario { message: String },
    #[error("invalid scenario step {step}: {message}")]
    InvalidStep { step: usize, message: String },
    #[error("scenario backend failed: {message}")]
    Backend { message: String },
    #[error("operation {operation_id} failed with {code}: {message}")]
    OperationFailed {
        operation_id: OperationId,
        code: String,
        message: String,
    },
    #[error("scenario assertion failed at step {step}: {message}")]
    AssertionFailed { step: usize, message: String },
    #[error("wait primitive {kind} is not available in Phase 1")]
    UnsupportedWait { kind: String },
}

pub trait ScenarioBackend {
    fn dispatch(&mut self, command: AppCommand) -> Result<OperationId, String>;
    fn wait_operation(&mut self, operation_id: OperationId) -> Result<OperationSnapshot, String>;
    fn state_dump(&mut self) -> Result<ModelSnapshot, String>;
    fn events_since(&mut self, sequence: u64) -> Result<Vec<AppEvent>, String>;
    fn terminal_contains(
        &mut self,
        terminal_id: TerminalId,
        text: &str,
        timeout_ms: u64,
    ) -> Result<(), String>;
    fn process_exit(&mut self, terminal_id: TerminalId, timeout_ms: u64) -> Result<(), String>;
}

pub struct ScenarioRunner<B> {
    backend: B,
    last_operation: Option<OperationId>,
    last_terminal: Option<TerminalId>,
    last_event_sequence: u64,
}

impl<B> ScenarioRunner<B>
where
    B: ScenarioBackend,
{
    pub fn new(backend: B) -> Self {
        Self {
            backend,
            last_operation: None,
            last_terminal: None,
            last_event_sequence: 0,
        }
    }

    pub fn run(&mut self, scenario: &Scenario) -> Result<(), ScenarioError> {
        for (step_index, step) in scenario.steps.iter().enumerate() {
            let action_count = usize::from(step.command.is_some())
                + usize::from(step.assert.is_some())
                + usize::from(step.wait.is_some());
            if action_count != 1 {
                return Err(ScenarioError::InvalidStep {
                    step: step_index,
                    message: "each step must contain exactly one of command, assert, or wait"
                        .to_owned(),
                });
            }

            if let Some(command) = &step.command {
                let operation_id = self
                    .backend
                    .dispatch(command.clone())
                    .map_err(|message| ScenarioError::Backend { message })?;
                self.last_operation = Some(operation_id);
                let operation = self
                    .backend
                    .wait_operation(operation_id)
                    .map_err(|message| ScenarioError::Backend { message })?;
                self.ensure_succeeded(operation_id, &operation)?;
                if let Some(OperationResult::TerminalSpawned { terminal_id }) = operation.result {
                    self.last_terminal = Some(terminal_id);
                }
            } else if let Some(assertion) = &step.assert {
                self.assert(step_index, assertion)?;
            } else if let Some(wait) = &step.wait {
                self.wait(step_index, wait)?;
            }
        }
        Ok(())
    }

    fn ensure_succeeded(
        &self,
        operation_id: OperationId,
        operation: &OperationSnapshot,
    ) -> Result<(), ScenarioError> {
        if operation.status == OperationStatus::Failed {
            let error = operation.error.clone().unwrap_or_else(|| {
                crate::command::CommandError::new("UNKNOWN", "operation failed without an error")
            });
            return Err(ScenarioError::OperationFailed {
                operation_id,
                code: error.code,
                message: error.message,
            });
        }
        Ok(())
    }

    fn wait(&mut self, step: usize, wait: &WaitPrimitive) -> Result<(), ScenarioError> {
        match wait {
            WaitPrimitive::OperationComplete { operation_id } => {
                let operation_id = operation_id.or(self.last_operation).ok_or_else(|| {
                    ScenarioError::InvalidStep {
                        step,
                        message: "operation_complete requires a prior command or operation_id"
                            .to_owned(),
                    }
                })?;
                let operation = self
                    .backend
                    .wait_operation(operation_id)
                    .map_err(|message| ScenarioError::Backend { message })?;
                self.ensure_succeeded(operation_id, &operation)
            }
            WaitPrimitive::Event { event_type } => {
                let events = self
                    .backend
                    .events_since(self.last_event_sequence)
                    .map_err(|message| ScenarioError::Backend { message })?;
                if let Some(last) = events.last() {
                    self.last_event_sequence = last.sequence;
                }
                if events
                    .iter()
                    .any(|event| event.kind.type_name() == event_type)
                {
                    Ok(())
                } else {
                    Err(ScenarioError::AssertionFailed {
                        step,
                        message: format!("event {event_type} was not observed"),
                    })
                }
            }
            WaitPrimitive::StateRevisionAtLeast { value } => {
                let state = self
                    .backend
                    .state_dump()
                    .map_err(|message| ScenarioError::Backend { message })?;
                if state.state_revision >= *value {
                    Ok(())
                } else {
                    Err(ScenarioError::AssertionFailed {
                        step,
                        message: format!(
                            "state revision {} is below required {value}",
                            state.state_revision
                        ),
                    })
                }
            }
            WaitPrimitive::AppIdle => {
                if let Some(operation_id) = self.last_operation {
                    let operation = self
                        .backend
                        .wait_operation(operation_id)
                        .map_err(|message| ScenarioError::Backend { message })?;
                    self.ensure_succeeded(operation_id, &operation)?;
                }
                Ok(())
            }
            WaitPrimitive::TerminalContains {
                terminal_id,
                text,
                timeout_ms,
            } => {
                let terminal_id = terminal_id.or(self.last_terminal).ok_or_else(|| {
                    ScenarioError::InvalidStep {
                        step,
                        message:
                            "terminal_contains requires a terminal_id or a prior terminal.spawn"
                                .to_owned(),
                    }
                })?;
                self.backend
                    .terminal_contains(terminal_id, text, *timeout_ms)
                    .map_err(|message| ScenarioError::Backend { message })
            }
            WaitPrimitive::ProcessExit {
                terminal_id,
                timeout_ms,
            } => {
                let terminal_id = terminal_id.or(self.last_terminal).ok_or_else(|| {
                    ScenarioError::InvalidStep {
                        step,
                        message: "process_exit requires a terminal_id or a prior terminal.spawn"
                            .to_owned(),
                    }
                })?;
                self.backend
                    .process_exit(terminal_id, *timeout_ms)
                    .map_err(|message| ScenarioError::Backend { message })
            }
        }
    }

    fn assert(&mut self, step: usize, assertion: &ScenarioAssertion) -> Result<(), ScenarioError> {
        let state = self
            .backend
            .state_dump()
            .map_err(|message| ScenarioError::Backend { message })?;
        let passed = match assertion {
            ScenarioAssertion::PaneCount { value } => active_tab(&state)
                .map(|tab| tab.tree.pane_count() == *value)
                .unwrap_or(*value == 0),
            ScenarioAssertion::TabCount { value } => state
                .workspace
                .as_ref()
                .map(|workspace| workspace.tabs.len() == *value)
                .unwrap_or(*value == 0),
            ScenarioAssertion::ActiveSurfaceKind { kind } => state
                .workspace
                .as_ref()
                .and_then(|workspace| {
                    workspace
                        .tabs
                        .iter()
                        .find(|tab| Some(tab.id) == workspace.active_tab)
                })
                .map(|tab| tab.tree.surface_kind_for(tab.active_pane) == Some(*kind))
                .unwrap_or(false),
            ScenarioAssertion::StateRevisionAtLeast { value } => state.state_revision >= *value,
        };
        if passed {
            Ok(())
        } else {
            Err(ScenarioError::AssertionFailed {
                step,
                message: format!("assertion {assertion:?} did not hold for state {state:?}"),
            })
        }
    }
}

fn active_tab(state: &ModelSnapshot) -> Option<&crate::app::model::TabDump> {
    let workspace = state.workspace.as_ref()?;
    let active_tab = workspace.active_tab?;
    workspace.tabs.iter().find(|tab| tab.id == active_tab)
}

pub struct InProcessBackend<'a> {
    pub dispatcher: &'a mut CommandDispatcher,
}

impl<'a> InProcessBackend<'a> {
    pub fn new(dispatcher: &'a mut CommandDispatcher) -> Self {
        Self { dispatcher }
    }
}

impl ScenarioBackend for InProcessBackend<'_> {
    fn dispatch(&mut self, command: AppCommand) -> Result<OperationId, String> {
        Ok(self.dispatcher.dispatch(command))
    }

    fn wait_operation(&mut self, operation_id: OperationId) -> Result<OperationSnapshot, String> {
        self.dispatcher
            .wait_operation(operation_id)
            .map_err(|error| error.to_string())
    }

    fn state_dump(&mut self) -> Result<ModelSnapshot, String> {
        self.dispatcher.pump_background_events();
        Ok(self.dispatcher.state_dump())
    }

    fn events_since(&mut self, sequence: u64) -> Result<Vec<AppEvent>, String> {
        self.dispatcher.pump_background_events();
        Ok(self.dispatcher.events_since(sequence))
    }

    fn terminal_contains(
        &mut self,
        terminal_id: TerminalId,
        text: &str,
        timeout_ms: u64,
    ) -> Result<(), String> {
        self.dispatcher
            .wait_terminal_contains(
                terminal_id,
                text,
                std::time::Duration::from_millis(timeout_ms),
            )
            .map(|_| ())
            .map_err(|error| error.to_string())
    }

    fn process_exit(&mut self, terminal_id: TerminalId, timeout_ms: u64) -> Result<(), String> {
        self.dispatcher
            .wait_terminal_exit(terminal_id, std::time::Duration::from_millis(timeout_ms))
            .map(|_| ())
            .map_err(|error| error.to_string())
    }
}

pub struct ControlBackend {
    pub client: ControlClient,
}

impl ControlBackend {
    pub fn new(client: ControlClient) -> Self {
        Self { client }
    }
}

impl ScenarioBackend for ControlBackend {
    fn dispatch(&mut self, command: AppCommand) -> Result<OperationId, String> {
        self.client
            .dispatch(command)
            .map_err(|error| error.to_string())
    }

    fn wait_operation(&mut self, operation_id: OperationId) -> Result<OperationSnapshot, String> {
        self.client
            .wait_operation(operation_id)
            .map_err(|error| error.to_string())
    }

    fn state_dump(&mut self) -> Result<ModelSnapshot, String> {
        self.client.state_dump().map_err(|error| error.to_string())
    }

    fn events_since(&mut self, sequence: u64) -> Result<Vec<AppEvent>, String> {
        self.client
            .events_since(sequence)
            .map_err(|error| error.to_string())
    }

    fn terminal_contains(
        &mut self,
        terminal_id: TerminalId,
        text: &str,
        timeout_ms: u64,
    ) -> Result<(), String> {
        self.client
            .terminal_contains(
                terminal_id,
                text,
                std::time::Duration::from_millis(timeout_ms),
            )
            .map(|_| ())
            .map_err(|error| error.to_string())
    }

    fn process_exit(&mut self, terminal_id: TerminalId, timeout_ms: u64) -> Result<(), String> {
        self.client
            .wait_terminal_exit(terminal_id, std::time::Duration::from_millis(timeout_ms))
            .map(|_| ())
            .map_err(|error| error.to_string())
    }
}

#[allow(dead_code)]
fn _tree_type_is_reachable(_tree: &PaneTreeDump) {}
