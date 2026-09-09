#![cfg(unix)]

use std::time::Duration;

use water::app::ModelHost;
use water::automation::{InProcessBackend, Scenario, ScenarioRunner};
use water::command::{
    AppCommand, CommandDispatcher, OperationResult, OperationStatus, PaneCommand, SplitDirection,
    TabCommand, TerminalCommand, WorkspaceCommand,
};
use water::event::AppEventKind;
use water::terminal::{
    TerminalColor, TerminalProcessState, default_shell_args, default_shell_program,
    snapshot_from_replay,
};

#[test]
fn new_tabs_and_split_panes_start_real_shell_terminals() {
    let mut dispatcher = CommandDispatcher::new();
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );

    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New {
        title: Some("First".to_owned()),
    }));
    assert_eq!(
        dispatcher.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );
    let state = dispatcher.state_dump();
    assert_terminal_tree(&state.workspace.as_ref().unwrap().tabs[0].tree);

    let split = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Split {
        pane_id: None,
        direction: SplitDirection::Right,
    }));
    assert_eq!(
        dispatcher.wait_operation(split).unwrap().status,
        OperationStatus::Succeeded
    );
    let state = dispatcher.state_dump();
    let tree = &state.workspace.as_ref().unwrap().tabs[0].tree;
    assert_eq!(tree.pane_count(), 2);
    assert_terminal_tree(tree);

    let second_tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New {
        title: Some("Second".to_owned()),
    }));
    assert_eq!(
        dispatcher.wait_operation(second_tab).unwrap().status,
        OperationStatus::Succeeded
    );
    let state = dispatcher.state_dump();
    assert_eq!(state.workspace.as_ref().unwrap().tabs.len(), 2);
    for tab in &state.workspace.as_ref().unwrap().tabs {
        assert_terminal_tree(&tab.tree);
    }
}

#[test]
fn configured_scrollback_limit_is_used_by_new_terminals() {
    let mut dispatcher = CommandDispatcher::with_scrollback_lines(123);
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
    assert_eq!(
        dispatcher.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );
    assert_eq!(dispatcher.memory_stats().scrollback_lines, 123);
}

#[test]
fn terminal_worker_captures_output_and_ansi_cell_attributes() {
    let mut dispatcher = CommandDispatcher::new();
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
    assert_eq!(
        dispatcher.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );

    let spawn = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
        pane_id: None,
        program: "/bin/sh".to_owned(),
        args: vec![
            "-c".to_owned(),
            "printf '\\033[31mRED\\033[0m \\033[1;3;4;9mSTYLE\\033[0m\\n'".to_owned(),
        ],
        columns: 80,
        lines: 24,
    }));
    let spawn = dispatcher.wait_operation(spawn).unwrap();
    assert_eq!(spawn.status, OperationStatus::Succeeded);
    let terminal_id = match spawn.result.unwrap() {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };

    let replay = dispatcher
        .wait_terminal_contains(terminal_id, "RED", Duration::from_secs(5))
        .expect("terminal output arrives");
    let snapshot = snapshot_from_replay(&replay, 2_000);
    assert_eq!(snapshot.cell(0, 0).unwrap().character, 'R');
    assert_eq!(
        snapshot.cell(0, 0).unwrap().fg,
        TerminalColor::Named { value: 1 }
    );
    let styled = &snapshot.cell(0, 4).unwrap().flags;
    assert!(styled.bold());
    assert!(styled.italic());
    assert!(styled.underline());
    assert!(styled.strike());
    let exit = dispatcher
        .wait_terminal_exit(terminal_id, Duration::from_secs(5))
        .unwrap();
    assert!(matches!(
        exit.process,
        TerminalProcessState::Exited { code: Some(0) }
    ));
}

#[test]
fn binary_terminal_output_does_not_leave_device_responses_in_zsh_input() {
    let shell = default_shell_program();
    if !std::path::Path::new(&shell).is_file() {
        eprintln!("skipping binary terminal output test: {shell} is not installed");
        return;
    }

    let mut dispatcher = CommandDispatcher::new();
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
    assert_eq!(
        dispatcher.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );
    let spawn = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
        pane_id: None,
        program: shell,
        args: vec!["-f".to_owned()],
        columns: 80,
        lines: 24,
    }));
    let terminal_id = match dispatcher.wait_operation(spawn).unwrap().result.unwrap() {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };

    let send_binary = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::SendText {
        terminal_id: Some(terminal_id),
        pane_id: None,
        text: r#"printf '\033[\023c\0'; printf 'BINARY_DONE\n'"#.to_owned() + "\n",
    }));
    assert_eq!(
        dispatcher.wait_operation(send_binary).unwrap().status,
        OperationStatus::Succeeded
    );
    let replay = dispatcher
        .wait_terminal_contains(terminal_id, "BINARY_DONE", Duration::from_secs(5))
        .expect("binary command completes");
    let snapshot = snapshot_from_replay(&replay, 2_000);
    assert!(!snapshot.visible_text().contains("6c"));

    let send_follow_up = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::SendText {
        terminal_id: Some(terminal_id),
        pane_id: None,
        text: r#"printf 'AFTER\n'"#.to_owned() + "\n",
    }));
    assert_eq!(
        dispatcher.wait_operation(send_follow_up).unwrap().status,
        OperationStatus::Succeeded
    );
    let replay = dispatcher
        .wait_terminal_contains(terminal_id, "AFTER", Duration::from_secs(5))
        .expect("follow-up command reaches zsh");
    let snapshot = snapshot_from_replay(&replay, 2_000);
    assert!(!snapshot.visible_text().contains("6c"));
}

#[test]
fn exited_terminal_closes_a_non_final_pane_automatically() {
    let mut dispatcher = CommandDispatcher::new();
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
    assert_eq!(
        dispatcher.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );

    let split = dispatcher.dispatch(AppCommand::Pane(PaneCommand::Split {
        pane_id: None,
        direction: SplitDirection::Right,
    }));
    let new_pane = match dispatcher.wait_operation(split).unwrap().result.unwrap() {
        OperationResult::PaneCreated { pane_id } => pane_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let spawn = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
        pane_id: Some(new_pane),
        program: "/bin/sh".to_owned(),
        args: vec!["-c".to_owned(), "exit 0".to_owned()],
        columns: 80,
        lines: 24,
    }));
    let terminal_id = match dispatcher.wait_operation(spawn).unwrap().result.unwrap() {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };

    dispatcher
        .wait_terminal_exit(terminal_id, Duration::from_secs(5))
        .expect("short-lived terminal exits");
    assert!(dispatcher.pump_background_events());

    let state = dispatcher.state_dump();
    let tab = &state.workspace.as_ref().unwrap().tabs[0];
    assert_eq!(tab.tree.pane_count(), 1);
    assert_ne!(tab.active_pane, new_pane);
    assert_eq!(dispatcher.memory_stats().terminal_count, 1);
    assert!(dispatcher.terminal_registry().replay(terminal_id).is_ok());
    assert!(dispatcher.all_events().iter().any(|event| matches!(
        event.kind,
        AppEventKind::PaneClosed { pane_id } if pane_id == new_pane
    )));

    dispatcher
        .terminal_registry()
        .wait_process_exit(terminal_id, Duration::from_secs(1))
        .expect("retired terminal remains waitable");
}

#[test]
fn exited_terminal_closes_the_final_pane_with_its_tab() {
    let mut dispatcher = CommandDispatcher::new();
    let workspace = dispatcher.dispatch(AppCommand::Workspace(WorkspaceCommand::Create));
    assert_eq!(
        dispatcher.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = dispatcher.dispatch(AppCommand::Tab(TabCommand::New { title: None }));
    let tab_id = match dispatcher.wait_operation(tab).unwrap().result.unwrap() {
        OperationResult::TabCreated { tab_id } => tab_id,
        result => panic!("unexpected result: {result:?}"),
    };
    let spawn = dispatcher.dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
        pane_id: None,
        program: "/bin/sh".to_owned(),
        args: vec!["-c".to_owned(), "exit 0".to_owned()],
        columns: 80,
        lines: 24,
    }));
    let terminal_id = match dispatcher.wait_operation(spawn).unwrap().result.unwrap() {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };

    dispatcher
        .wait_terminal_exit(terminal_id, Duration::from_secs(5))
        .expect("short-lived terminal exits");
    assert!(dispatcher.pump_background_events());

    let state = dispatcher.state_dump();
    let workspace = state.workspace.as_ref().unwrap();
    assert!(workspace.tabs.is_empty());
    assert_eq!(workspace.active_tab, None);
    assert_eq!(dispatcher.memory_stats().terminal_count, 0);
    assert!(dispatcher.all_events().iter().any(|event| matches!(
        event.kind,
        AppEventKind::TabClosed { tab_id: closed_tab_id } if closed_tab_id == tab_id
    )));
}

#[test]
fn terminal_scenario_waits_on_output_and_process_exit() {
    let path = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/scenarios/terminal_basic.json");
    let scenario = Scenario::from_path(path).unwrap();
    let mut dispatcher = CommandDispatcher::new();
    ScenarioRunner::new(InProcessBackend::new(&mut dispatcher))
        .run(&scenario)
        .unwrap();
}

#[test]
fn homebrew_zsh_runs_as_an_interactive_terminal_process() {
    let zsh = std::path::Path::new(water::terminal::HOMEBREW_ZSH);
    if !zsh.is_file() {
        eprintln!(
            "skipping Homebrew zsh test: {} is not installed",
            zsh.display()
        );
        return;
    }

    let path = std::path::PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("tests/scenarios/terminal_zsh.json");
    let scenario = Scenario::from_path(path).unwrap();
    let mut dispatcher = CommandDispatcher::new();
    ScenarioRunner::new(InProcessBackend::new(&mut dispatcher))
        .run(&scenario)
        .unwrap();
}

fn assert_terminal_tree(tree: &water::app::model::PaneTreeDump) {
    match tree {
        water::app::model::PaneTreeDump::Leaf { surface_state, .. } => {
            let water::surface::SurfaceState::Terminal(terminal) = surface_state else {
                panic!("new pane did not receive a terminal: {surface_state:?}");
            };
            assert_eq!(terminal.program, default_shell_program());
            assert_eq!(terminal.args, default_shell_args(&terminal.program));
            assert_eq!(terminal.columns, 80);
            assert_eq!(terminal.lines, 24);
        }
        water::app::model::PaneTreeDump::Split { first, second, .. } => {
            assert_terminal_tree(first);
            assert_terminal_tree(second);
        }
    }
}

#[test]
fn model_host_projects_terminal_snapshots_without_mutable_ui_access() {
    let mut host = ModelHost::start();
    let client = host.client();
    let workspace = client
        .dispatch(AppCommand::Workspace(WorkspaceCommand::Create))
        .unwrap();
    assert_eq!(
        client.wait_operation(workspace).unwrap().status,
        OperationStatus::Succeeded
    );
    let tab = client
        .dispatch(AppCommand::Tab(TabCommand::New { title: None }))
        .unwrap();
    assert_eq!(
        client.wait_operation(tab).unwrap().status,
        OperationStatus::Succeeded
    );
    let spawn = client
        .dispatch(AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: None,
            program: "/bin/sh".to_owned(),
            args: vec![
                "-c".to_owned(),
                "printf MODEL_HOST_READY; read line".to_owned(),
            ],
            columns: 40,
            lines: 8,
        }))
        .unwrap();
    let spawn = client.wait_operation(spawn).unwrap();
    let terminal_id = match spawn.result.unwrap() {
        OperationResult::TerminalSpawned { terminal_id } => terminal_id,
        result => panic!("unexpected result: {result:?}"),
    };
    client
        .terminal_contains(terminal_id, "MODEL_HOST_READY", Duration::from_secs(5))
        .unwrap();
    let state = client.state_dump().unwrap();
    let tree = &state.workspace.unwrap().tabs[0].tree;
    assert!(matches!(
        tree,
        water::app::model::PaneTreeDump::Leaf {
            terminal: Some(_),
            ..
        }
    ));
    let replay = client.terminal_replay(terminal_id).unwrap();
    let snapshot = snapshot_from_replay(&replay, 2_000);
    assert_eq!(snapshot.size.columns, 40);
    assert_eq!(snapshot.size.lines, 8);
    assert!(snapshot.visible_text().contains("MODEL_HOST_READY"));

    let send = client
        .dispatch(AppCommand::Terminal(TerminalCommand::SendText {
            terminal_id: Some(terminal_id),
            pane_id: None,
            text: "\n".to_owned(),
        }))
        .unwrap();
    assert_eq!(
        client.wait_operation(send).unwrap().status,
        OperationStatus::Succeeded
    );
    client
        .wait_terminal_exit(terminal_id, Duration::from_secs(5))
        .unwrap();
    host.shutdown();
}
