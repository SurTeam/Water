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
            terminal: Some(projection),
            ..
        } => projection.snapshot.as_deref(),
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => {
            first_terminal_snapshot(first).or_else(|| first_terminal_snapshot(second))
        }
    }
}

fn first_terminal_summary(tree: &PaneTreeDump) -> Option<&water::terminal::TerminalSummary> {
    match tree {
        PaneTreeDump::Leaf {
            terminal: Some(projection),
            ..
        } => Some(&projection.summary),
        PaneTreeDump::Leaf { .. } => None,
        PaneTreeDump::Split { first, second, .. } => {
            first_terminal_summary(first).or_else(|| first_terminal_summary(second))
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
                first_terminal_summary(&tab.tree).is_some(),
                "workspace {} tab {} lacks a terminal summary",
                workspace.id,
                tab.id
            );
            // Grid cells are projected for the displayed tab of every
            // workspace; hidden tabs stay metadata-only.
            if workspace.active_tab == Some(tab.id) {
                assert!(
                    first_terminal_snapshot(&tab.tree).is_some(),
                    "displayed tab {} lacks a terminal grid",
                    tab.id
                );
            }
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
fn hidden_tabs_project_summaries_only_and_fetch_grids_per_terminal() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    let first_tab = match dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    ) {
        OperationResult::TabCreated { tab_id } => tab_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let first_terminal = dispatcher
        .state_dump()
        .workspace
        .as_ref()
        .and_then(|workspace| workspace.tabs.iter().find(|tab| tab.id == first_tab))
        .and_then(|tab| first_terminal_summary(&tab.tree))
        .map(|summary| summary.terminal_id)
        .expect("first tab projects a terminal summary");
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );

    let state = dispatcher.state_dump();
    let workspace = state.workspace.as_ref().unwrap();
    let hidden = workspace
        .tabs
        .iter()
        .find(|tab| tab.id == first_tab)
        .expect("hidden tab present");
    assert_eq!(workspace.active_tab, Some(workspace.tabs[1].id));
    let summary = first_terminal_summary(&hidden.tree).expect("hidden tab keeps a summary");
    assert_eq!(summary.terminal_id, first_terminal);
    assert!(
        first_terminal_snapshot(&hidden.tree).is_none(),
        "hidden tabs must not duplicate grid cells into the projection"
    );

    // The registry remains the single source of truth for hidden grids.
    let fetched = dispatcher.terminal_snapshot(first_terminal).unwrap();
    assert_eq!(fetched.terminal_id, first_terminal);
    assert_eq!(fetched.size.columns, 80);
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

#[test]
fn background_tab_scrollback_is_trimmed_to_the_inactive_tail() {
    let mut config = AppConfig::default();
    config.terminal.scrollback_lines = 100;
    config.terminal.inactive_scrollback_lines = 10;
    let mut dispatcher = CommandDispatcher::with_config(config);
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let first_terminal = {
        let state = dispatcher.state_dump();
        let tab = &state.workspace.as_ref().unwrap().tabs[0];
        first_terminal_summary(&tab.tree)
            .expect("first tab projects a summary")
            .terminal_id
    };
    // Opening and focusing a second tab demotes the first one.
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );

    let flood = dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(first_terminal),
            pane_id: None,
            text: "for i in $(seq 1 200); do echo BACKGROUND_$i; done\n".to_owned(),
        }),
    );
    let _ = flood;
    dispatcher
        .wait_terminal_contains(first_terminal, "BACKGROUND_200", Duration::from_secs(10))
        .expect("background tab output reaches the registry");

    let stats = dispatcher.memory_stats();
    // Without focus-aware trimming this background tab alone would retain
    // its full 100-row limit; the cap below only holds if the worker honored
    // the 10-row inactive tail (plus room for the focused tab's startup).
    assert!(
        stats.retained_scrollback_lines <= 40,
        "background tab must stay at the inactive tail: {stats:?}"
    );
    assert_eq!(stats.scrollback_lines, 100);
    assert_eq!(stats.inactive_scrollback_lines, 10);
}

fn focused_terminal_id(dispatcher: &CommandDispatcher) -> water::ids::TerminalId {
    let state = dispatcher.state_dump();
    let workspace = state.workspace.as_ref().expect("active workspace");
    let tab = &workspace.tabs[workspace.tabs.len() - 1];
    first_terminal_summary(&tab.tree)
        .expect("active tab projects a summary")
        .terminal_id
}

fn send_text(dispatcher: &mut CommandDispatcher, terminal_id: water::ids::TerminalId, text: &str) {
    dispatch_ok(
        dispatcher,
        AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: text.to_owned(),
        }),
    );
}

#[test]
fn absolute_viewport_request_scrolls_real_shell_history_and_acks() {
    let mut config = AppConfig::default();
    config.terminal.scrollback_lines = 500;
    let mut dispatcher = CommandDispatcher::with_config(config);
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let terminal_id = focused_terminal_id(&dispatcher);
    let registry = dispatcher.terminal_registry();

    send_text(
        &mut dispatcher,
        terminal_id,
        "for i in $(seq 1 80); do echo VIEWPORT_LINE_$i; done\n",
    );
    let bottom = registry
        .contains_text(terminal_id, "VIEWPORT_LINE_80", Duration::from_secs(10))
        .expect("history reaches the terminal");
    assert!(bottom.rows_before.len() >= 3);

    let target = bottom.viewport_position + 3;
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::SetViewportPosition {
            terminal_id: Some(terminal_id),
            pane_id: None,
            target,
        }),
    );
    let scrolled = registry
        .wait_viewport_position(terminal_id, target, Duration::from_secs(2))
        .expect("worker acknowledges absolute viewport target");
    assert!(std::sync::Arc::ptr_eq(
        &scrolled.rows[0],
        &bottom.rows_before[2]
    ));

    dispatch_ok(
        &mut dispatcher,
        AppCommand::Terminal(TerminalCommand::SetViewportPosition {
            terminal_id: Some(terminal_id),
            pane_id: None,
            target: bottom.viewport_position,
        }),
    );
    registry
        .wait_viewport_position(
            terminal_id,
            bottom.viewport_position,
            Duration::from_secs(2),
        )
        .expect("worker returns to the bottom viewport");
}

#[test]
fn clear_command_erases_scrollback_via_shell_integration() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let terminal_id = focused_terminal_id(&dispatcher);
    let registry = dispatcher.terminal_registry();

    send_text(
        &mut dispatcher,
        terminal_id,
        "for i in $(seq 1 30); do echo SCROLLBACK_LINE$i; done\n",
    );
    registry
        .contains_text(terminal_id, "SCROLLBACK_LINE30", Duration::from_secs(10))
        .expect("flood reaches the terminal");

    // The marker must be distinguishable from the shell's raw input echo:
    // the typed command shows `RESULT_$((6*7))` while only the executed
    // output contains `RESULT_42`. The bundled terminfo keeps the standard
    // clear capability; Water's zsh integration wraps the `clear` command
    // itself with CSI 3 J.
    send_text(
        &mut dispatcher,
        terminal_id,
        "clear; echo RESULT_$((6*7))\n",
    );
    registry
        .contains_text(terminal_id, "RESULT_42", Duration::from_secs(10))
        .expect("clear and marker executed");

    let snapshot = registry.snapshot(terminal_id).unwrap();
    let visible = snapshot.visible_text();
    assert!(visible.contains("RESULT_42"));
    assert!(
        !visible.contains("SCROLLBACK_LINE15"),
        "cleared output must not remain in the viewport: {visible}"
    );
    assert!(
        snapshot.rows_before.is_empty(),
        "clear must erase scrollback; the E3 append comes from the zsh integration"
    );
}

#[test]
fn ctrl_l_pushes_the_prompt_to_the_top_without_erasing_scrollback() {
    let mut dispatcher = CommandDispatcher::new();
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::New { title: None }),
    );
    let terminal_id = focused_terminal_id(&dispatcher);
    let registry = dispatcher.terminal_registry();

    send_text(
        &mut dispatcher,
        terminal_id,
        "for i in $(seq 1 30); do echo CTRL_L_HISTORY$i; done\n",
    );
    registry
        .contains_text(terminal_id, "CTRL_L_HISTORY30", Duration::from_secs(10))
        .expect("flood reaches the terminal");

    // Ctrl-L goes through zle's clear-screen widget, which emits the
    // terminfo `clear` string. That string must stay free of CSI 3 J so
    // the previous screen is only pushed into scrollback.
    send_text(&mut dispatcher, terminal_id, "\u{c}");
    send_text(&mut dispatcher, terminal_id, "echo CTRL_L_KEPT_$((6*7))\n");
    registry
        .contains_text(terminal_id, "CTRL_L_KEPT_42", Duration::from_secs(10))
        .expect("post-ctrl-l marker executed");

    let snapshot = registry.snapshot(terminal_id).unwrap();
    let visible = snapshot.visible_text();
    assert!(visible.contains("CTRL_L_KEPT_42"));
    assert!(
        !visible.contains("CTRL_L_HISTORY15"),
        "the viewport should be redrawn with the prompt near the top: {visible}"
    );
    assert!(
        !snapshot.rows_before.is_empty(),
        "ctrl-l must keep the previous screen in scrollback (terminfo clear must not carry CSI 3J)"
    );
}

#[test]
fn closing_tabs_releases_retained_scrollback_memory() {
    let mut config = AppConfig::default();
    config.terminal.scrollback_lines = 2000;
    config.terminal.inactive_scrollback_lines = 500;
    let mut dispatcher = CommandDispatcher::with_config(config);
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Workspace(WorkspaceCommand::Create),
    );
    let mut terminals = Vec::new();
    for prefix in ["T1", "T2"] {
        dispatch_ok(
            &mut dispatcher,
            AppCommand::Tab(TabCommand::New { title: None }),
        );
        let terminal_id = focused_terminal_id(&dispatcher);
        send_text(
            &mut dispatcher,
            terminal_id,
            &format!("for i in $(seq 1 3000); do echo {prefix}FLOOD$i; done\n"),
        );
        dispatcher
            .terminal_registry()
            .contains_text(
                terminal_id,
                &format!("{prefix}FLOOD3000"),
                Duration::from_secs(15),
            )
            .expect("flood reaches the terminal");
        terminals.push(terminal_id);
    }

    let before = dispatcher.memory_stats();
    assert!(before.retained_scrollback_bytes > 4_000_000, "{before:?}");

    let first_tab = {
        let state = dispatcher.state_dump();
        state.workspace.as_ref().unwrap().tabs[0].id
    };
    dispatch_ok(
        &mut dispatcher,
        AppCommand::Tab(TabCommand::Close {
            tab_id: Some(first_tab),
        }),
    );

    let after = dispatcher.memory_stats();
    assert!(
        after.retained_scrollback_bytes < before.retained_scrollback_bytes,
        "closed tabs must release their scrollback budget: {before:?} -> {after:?}"
    );
    assert!(
        after.registry_snapshot_bytes < before.registry_snapshot_bytes,
        "closed tabs must release their registry snapshots: {before:?} -> {after:?}"
    );
    // The surviving tab keeps its own (focused) scrollback — that is the
    // remaining budget, not a leak.
    assert!(
        dispatcher.terminal_registry().terminal_count() == 1,
        "only the surviving tab keeps a registry entry"
    );
    assert!(
        after.retained_scrollback_bytes >= 3_000_000,
        "the focused surviving tab keeps its history: {after:?}"
    );
}
