use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Condvar, Mutex};

use serde::{Deserialize, Serialize};

use crate::ids::OperationId;

use super::{AppCommand, CommandError, OperationResult};

const MAX_RETAINED_OPERATIONS: usize = 4096;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum OperationStatus {
    Pending,
    Running,
    Succeeded,
    Failed,
}

impl OperationStatus {
    pub fn is_terminal(self) -> bool {
        matches!(self, Self::Succeeded | Self::Failed)
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct OperationSnapshot {
    pub id: OperationId,
    pub command: AppCommand,
    pub status: OperationStatus,
    pub result: Option<OperationResult>,
    pub error: Option<CommandError>,
}

#[derive(Debug, Clone)]
pub(crate) struct OperationRegistry {
    entries: Arc<Mutex<std::collections::BTreeMap<OperationId, Arc<OperationCell>>>>,
    next_id: Arc<AtomicU64>,
}

#[derive(Debug)]
struct OperationCell {
    snapshot: Mutex<OperationSnapshot>,
    completed: Condvar,
}

impl Default for OperationRegistry {
    fn default() -> Self {
        Self::new()
    }
}

impl OperationRegistry {
    pub(crate) fn new() -> Self {
        Self {
            entries: Arc::new(Mutex::new(std::collections::BTreeMap::new())),
            next_id: Arc::new(AtomicU64::new(1)),
        }
    }

    pub(crate) fn begin(&self, command: AppCommand) -> OperationId {
        let raw_id = self.next_id.fetch_add(1, Ordering::Relaxed);
        let id = OperationId::new(raw_id);
        let snapshot = OperationSnapshot {
            id,
            command,
            status: OperationStatus::Pending,
            result: None,
            error: None,
        };
        let cell = Arc::new(OperationCell {
            snapshot: Mutex::new(snapshot),
            completed: Condvar::new(),
        });
        let mut entries = self.entries.lock().expect("operation registry poisoned");
        entries.insert(id, cell);
        while entries.len() > MAX_RETAINED_OPERATIONS {
            let completed_id = entries.iter().find_map(|(candidate_id, cell)| {
                let snapshot = cell.snapshot.lock().expect("operation cell poisoned");
                snapshot.status.is_terminal().then_some(*candidate_id)
            });
            let Some(completed_id) = completed_id else {
                break;
            };
            entries.remove(&completed_id);
        }
        id
    }

    pub(crate) fn start(&self, id: OperationId) {
        if let Some(cell) = self.cell(id) {
            let mut snapshot = cell.snapshot.lock().expect("operation cell poisoned");
            snapshot.status = OperationStatus::Running;
        }
    }

    pub(crate) fn finish(&self, id: OperationId, result: Result<OperationResult, CommandError>) {
        if let Some(cell) = self.cell(id) {
            let mut snapshot = cell.snapshot.lock().expect("operation cell poisoned");
            match result {
                Ok(result) => {
                    snapshot.status = OperationStatus::Succeeded;
                    snapshot.result = Some(result);
                    snapshot.error = None;
                }
                Err(error) => {
                    snapshot.status = OperationStatus::Failed;
                    snapshot.result = None;
                    snapshot.error = Some(error);
                }
            }
            cell.completed.notify_all();
        }
    }

    pub(crate) fn get(&self, id: OperationId) -> Option<OperationSnapshot> {
        self.cell(id).map(|cell| {
            cell.snapshot
                .lock()
                .expect("operation cell poisoned")
                .clone()
        })
    }

    pub(crate) fn wait(&self, id: OperationId) -> Option<OperationSnapshot> {
        let cell = self.cell(id)?;
        let mut snapshot = cell.snapshot.lock().expect("operation cell poisoned");
        while !snapshot.status.is_terminal() {
            snapshot = cell
                .completed
                .wait(snapshot)
                .expect("operation cell poisoned");
        }
        Some(snapshot.clone())
    }

    fn cell(&self, id: OperationId) -> Option<Arc<OperationCell>> {
        self.entries
            .lock()
            .expect("operation registry poisoned")
            .get(&id)
            .cloned()
    }
}
