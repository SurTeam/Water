#![cfg(unix)]

use std::path::Path;
use std::time::Duration;

use water::app::model::PaneTreeDump;
use water::command::{
    AppCommand, CommandDispatcher, OperationResult, OperationStatus, PaneCommand, TabCommand,
    TerminalCommand, WorkspaceCommand,
};
use water::config::AppConfig;
use water::surface::SurfaceState;

fn dispatch_ok(dispatcher: &mut CommandDispatcher, command: AppCommand) -> OperationResult {
    let operation_id = dispatcher.dispatch(command);
    let operation = dispatcher.wait_operation(operation_id).unwrap();
    assert_eq!(operation.status, OperationStatus::Succeeded);
    operation.result.unwrap()
}

fn first_terminal(tree: &PaneTreeDump) -> Option<(water::ids::PaneId, water::ids::TerminalId)> {
    match tree {
        PaneTreeDump::Leaf {
            pane_id,
            surface_state: SurfaceState::Terminal(terminal),
            ..
        } => Some((*pane_id, terminal.terminal_id)),
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => {
            first_terminal(first).or_else(|| first_terminal(second))
        }
    }
}

fn first_terminal_snapshot(tree: &PaneTreeDump) -> Option<&water::terminal::TerminalSnapshot> {
    match tree {
        PaneTreeDump::Leaf {
            terminal_snapshot: Some(snapshot),
            ..
        } => Some(snapshot),
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => {
            first_terminal_snapshot(first).or_else(|| first_terminal_snapshot(second))
        }
    }
}

#[test]
fn workspaces_can_be_created_activated_renamed_and_deleted() {
    let mut dispatcher = CommandDispatcher::new();
    let first = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::New),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let second = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::New),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };

    let state = dispatcher.state_dump();
    assert_eq!(state.workspaces.len(), 2);
    assert_eq!(state.active_workspace, Some(second));
    assert_eq!(state.workspaces[0].title, "Workspace 1");
    assert_eq!(state.workspaces[1].title, "Workspace 2");
    assert_eq!(state.workspaces[0].tabs.len(), 1);
    assert_eq!(state.workspaces[1].tabs.len(), 1);

    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Rename {
            workspace_id: Some(second),
            title: "Build".to_owned(),
        }),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Activate {
            workspace_id: Some(first),
        }),
    );
    assert_eq!(dispatcher.state_dump().active_workspace, Some(first));

    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Delete {
            workspace_id: Some(first),
        }),
    );
    let state = dispatcher.state_dump();
    assert_eq!(state.workspaces.len(), 1);
    assert_eq!(state.active_workspace, Some(second));
    assert_eq!(state.workspaces[0].title, "Build");
    assert_eq!(dispatcher.memory_stats().terminal_count, 1);
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Delete {
            workspace_id: Some(second),
        }),
    );
    assert_eq!(dispatcher.memory_stats().terminal_count, 0);
}

#[test]
fn new_workspace_has_one_configured_default_terminal_tab() {
    let mut config = AppConfig::default();
    config.shell.program = "/bin/sh".to_owned();
    config.shell.args = vec!["-c".to_owned(), "exec sleep 30".to_owned()];
    config.terminal.default_columns = 37;
    config.terminal.default_lines = 9;
    let mut dispatcher = CommandDispatcher::with_config(config);

    let workspace_id = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::New),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let state = dispatcher.state_dump();
    assert_eq!(state.active_workspace, Some(workspace_id));
    let workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == workspace_id)
        .expect("new workspace is projected");
    assert_eq!(workspace.tabs.len(), 1);
    assert_eq!(workspace.active_tab, Some(workspace.tabs[0].id));
    let PaneTreeDump::Leaf {
        surface_state: SurfaceState::Terminal(terminal),
        ..
    } = &workspace.tabs[0].tree
    else {
        panic!("new workspace did not contain one terminal tab");
    };
    assert_eq!(terminal.program, "/bin/sh");
    assert_eq!(terminal.args, ["-c", "exec sleep 30"]);
    assert_eq!(terminal.columns, 37);
    assert_eq!(terminal.lines, 9);
    assert_eq!(dispatcher.memory_stats().terminal_count, 1);
}

#[test]
fn failed_new_workspace_rolls_back_model_terminals_and_events() {
    let mut config = AppConfig::default();
    config.shell.program = "/definitely/not/a/real/water-shell".to_owned();
    let mut dispatcher = CommandDispatcher::with_config(config);

    let operation_id = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::New));
    let operation = dispatcher.wait_operation(operation_id).unwrap();
    assert_eq!(operation.status, OperationStatus::Failed);
    assert_eq!(operation.error.unwrap().code, "TERMINAL_SPAWN_FAILED");
    let state = dispatcher.state_dump();
    assert!(state.workspaces.is_empty());
    assert_eq!(state.workspace, None);
    assert_eq!(state.active_workspace, None);
    assert_eq!(dispatcher.memory_stats().terminal_count, 0);
    assert!(dispatcher.all_events().is_empty());
}

#[test]
fn new_in_workspace_targets_an_inactive_workspace_and_inherits_its_cwd() {
    let mut config = AppConfig::default();
    config.shell.program = "/bin/sh".to_owned();
    config.shell.args = vec!["-c".to_owned(), "exec sleep 30".to_owned()];
    config.startup.default_cwd = Some("/".to_owned());
    let mut dispatcher = CommandDispatcher::with_config(config);
    let first = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let first_tab = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    ) {
        OperationResult::TabCreated { tab_id } => tab_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let second = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let second_pane = dispatcher
        .state_dump()
        .workspaces
        .iter()
        .find(|workspace| workspace.id == second)
        .and_then(|workspace| workspace.tabs.first())
        .map(|tab| tab.active_pane)
        .expect("second workspace has its compatibility tab");
    let second_terminal = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(second_pane),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "cd /tmp; sleep 1; printf 'WATER_TARGET_CWD_READY\\n'; exec sleep 30".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    ) {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatcher
        .wait_terminal_contains(
            second_terminal,
            "WATER_TARGET_CWD_READY",
            Duration::from_secs(5),
        )
        .unwrap();
    dispatcher.pump_background_events();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Activate {
            workspace_id: Some(first),
        }),
    );

    let new_tab = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::NewInWorkspace {
            workspace_id: second,
            title: Some("Target".to_owned()),
        }),
    ) {
        OperationResult::TabCreated { tab_id } => tab_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let state = dispatcher.state_dump();
    assert_eq!(state.active_workspace, Some(first));
    let first_workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == first)
        .unwrap();
    assert_eq!(first_workspace.tabs.len(), 1);
    assert_eq!(first_workspace.tabs[0].id, first_tab);
    let second_workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == second)
        .unwrap();
    assert_eq!(second_workspace.tabs.len(), 2);
    assert_eq!(second_workspace.active_tab, Some(new_tab));
    let new_tab = second_workspace
        .tabs
        .iter()
        .find(|tab| tab.id == new_tab)
        .unwrap();
    let PaneTreeDump::Leaf {
        surface_state: SurfaceState::Terminal(terminal),
        ..
    } = &new_tab.tree
    else {
        panic!("targeted tab did not contain a terminal");
    };
    let expected_cwd = std::fs::canonicalize(Path::new("/tmp"))
        .unwrap()
        .display()
        .to_string();
    assert_eq!(terminal.cwd, expected_cwd);
}

#[test]
fn all_workspace_projections_include_terminal_snapshots() {
    let mut dispatcher = CommandDispatcher::new();
    let first = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let second = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    ) {
        OperationResult::WorkspaceCreated { workspace_id } => workspace_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let second_pane = dispatcher
        .state_dump()
        .workspaces
        .iter()
        .find(|workspace| workspace.id == second)
        .and_then(|workspace| workspace.tabs.first())
        .map(|tab| tab.active_pane)
        .unwrap();
    let second_terminal = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(second_pane),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "printf 'WATER_INACTIVE_SNAPSHOT\\n'; exec sleep 30".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    ) {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatcher
        .wait_terminal_contains(
            second_terminal,
            "WATER_INACTIVE_SNAPSHOT",
            Duration::from_secs(5),
        )
        .unwrap();
    dispatcher.pump_background_events();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Activate {
            workspace_id: Some(first),
        }),
    );

    let state = dispatcher.state_dump();
    assert_eq!(state.active_workspace, Some(first));
    assert_eq!(state.workspace.as_ref().unwrap().id, first);
    for workspace in &state.workspaces {
        for tab in &workspace.tabs {
            assert!(
                first_terminal_snapshot(&tab.tree).is_some(),
                "workspace {} tab {} lacks a terminal snapshot",
                workspace.id,
                tab.id
            );
        }
    }
    let second_workspace = state
        .workspaces
        .iter()
        .find(|workspace| workspace.id == second)
        .unwrap();
    let second_snapshot = first_terminal_snapshot(&second_workspace.tabs[0].tree).unwrap();
    assert!(
        second_snapshot
            .visible_text()
            .contains("WATER_INACTIVE_SNAPSHOT")
    );
}

#[test]
fn process_metadata_drives_titles_and_cwd_inheritance_until_explicit_rename() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let state = dispatcher.state_dump();
    let tab_id = state.workspace.as_ref().unwrap().tabs[0].id;
    let pane_id = state.workspace.as_ref().unwrap().tabs[0].active_pane;
    let terminal = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "cd /tmp; printf 'WATER_META_READY\\n'; sleep 1; printf 'WATER_META_DONE\\n'; exec sleep 30"
                    .to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    ) {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };

    let snapshot = dispatcher
        .wait_terminal_contains(terminal, "WATER_META_DONE", Duration::from_secs(5))
        .unwrap();
    let expected_cwd = std::fs::canonicalize(Path::new("/tmp"))
        .unwrap()
        .display()
        .to_string();
    assert_eq!(snapshot.cwd, expected_cwd);
    assert!(!snapshot.process_name.is_empty());
    assert!(dispatcher.pump_background_events());

    let state = dispatcher.state_dump();
    let active_tab = state
        .workspace
        .as_ref()
        .unwrap()
        .tabs
        .iter()
        .find(|tab| tab.id == tab_id)
        .unwrap();
    let terminal_surface = match &active_tab.tree {
        PaneTreeDump::Leaf {
            surface_state: SurfaceState::Terminal(terminal),
            ..
        } => terminal,
        tree => panic!("expected a leaf terminal, got {tree:?}"),
    };
    assert_eq!(terminal_surface.cwd, expected_cwd);
    assert_eq!(active_tab.title, terminal_surface.process_name);

    let inherited_tab = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    ) {
        OperationResult::TabCreated { tab_id } => tab_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let state = dispatcher.state_dump();
    let inherited = state
        .workspace
        .as_ref()
        .unwrap()
        .tabs
        .iter()
        .find(|tab| tab.id == inherited_tab)
        .unwrap();
    let (_, inherited_terminal) = first_terminal(&inherited.tree).unwrap();
    let inherited_surface = match &inherited.tree {
        PaneTreeDump::Leaf {
            surface_state: SurfaceState::Terminal(terminal),
            ..
        } => terminal,
        tree => panic!("expected a leaf terminal, got {tree:?}"),
    };
    assert_eq!(inherited_surface.cwd, expected_cwd);

    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::Rename {
            tab_id: Some(inherited_tab),
            title: "Locked".to_owned(),
        }),
    );
    let replaced = dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(inherited.active_pane),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "cd /; printf 'WATER_LOCKED_DONE\\n'; exec sleep 30".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    );
    let replaced_terminal = match replaced {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };
    dispatcher
        .wait_terminal_contains(
            replaced_terminal,
            "WATER_LOCKED_DONE",
            Duration::from_secs(5),
        )
        .unwrap();
    dispatcher.pump_background_events();
    let state = dispatcher.state_dump();
    let locked = state
        .workspace
        .as_ref()
        .unwrap()
        .tabs
        .iter()
        .find(|tab| tab.id == inherited_tab)
        .unwrap();
    assert_eq!(locked.title, "Locked");

    // Keep the original terminal ID live long enough to make sure the worker
    // that supplied the cwd was retired when its pane was replaced.
    assert!(
        dispatcher
            .terminal_registry()
            .snapshot(inherited_terminal)
            .is_err()
    );
}

#[test]
fn closing_a_pane_terminates_the_terminal_process_group() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let pane_id = dispatcher.state_dump().focused_pane.unwrap();
    let terminal_id = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "sleep 30 & child=$!; printf 'WATER_CHILD_%s\\n' \"$child\"; wait".to_owned(),
            ],
            columns: 80,
            lines: 24,
        }),
    ) {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let snapshot = dispatcher
        .wait_terminal_contains(terminal_id, "WATER_CHILD_", Duration::from_secs(5))
        .unwrap();
    let child_pid = snapshot
        .visible_text()
        .lines()
        .find_map(|line| {
            line.strip_prefix("WATER_CHILD_")?
                .trim()
                .parse::<libc::pid_t>()
                .ok()
        })
        .expect("child PID is printed");
    assert!(child_pid > 1);

    dispatch_ok(
        &mut dispatcher,
        AppCommand::Pane(PaneCommand::Close {
            pane_id: Some(pane_id),
        }),
    );

    let deadline = std::time::Instant::now() + Duration::from_secs(2);
    while std::time::Instant::now() < deadline {
        let alive = unsafe { libc::kill(child_pid, 0) == 0 };
        if !alive {
            break;
        }
        std::thread::yield_now();
    }
    let alive = unsafe { libc::kill(child_pid, 0) == 0 };
    if alive {
        unsafe {
            libc::kill(child_pid, libc::SIGKILL);
        }
    }
    assert!(!alive, "terminal descendant process survived pane close");
    assert!(
        dispatcher
            .terminal_registry()
            .snapshot(terminal_id)
            .is_err()
    );
}
