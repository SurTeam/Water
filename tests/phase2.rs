#![cfg(unix)]

use std::time::Duration;

use water::app::ModelHost;
use water::automation::{InProcessBackend, Scenario, ScenarioRunner};
use water::command::{
    AppCommand, CommandDispatcher, OperationResult, OperationStatus, PaneCommand, SplitDirection,
    TabCommand, TerminalCommand, WorkspaceCommand,
};
use water::terminal::{
    TerminalColor, TerminalProcessState, default_shell_args, default_shell_program,
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

    let snapshot = dispatcher
        .wait_terminal_contains(terminal_id, "RED", Duration::from_secs(5))
        .expect("terminal output arrives");
    assert_eq!(snapshot.cell(0, 0).unwrap().character, 'R');
    assert_eq!(
        snapshot.cell(0, 0).unwrap().fg,
        TerminalColor::Named { value: 1 }
    );
    let styled = &snapshot.cell(0, 4).unwrap().flags;
    assert!(styled.bold);
    assert!(styled.italic);
    assert!(styled.underline);
    assert!(styled.strike);
    let exit = dispatcher
        .wait_terminal_exit(terminal_id, Duration::from_secs(5))
        .unwrap();
    assert!(matches!(
        exit.process,
        TerminalProcessState::Exited { code: Some(0) }
    ));
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
            args: vec!["-c".to_owned(), "printf MODEL_HOST_READY".to_owned()],
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
    let snapshot = match tree {
        water::app::model::PaneTreeDump::Leaf {
            terminal_snapshot: Some(snapshot),
            ..
        } => snapshot,
        tree => panic!("expected terminal leaf, got {tree:?}"),
    };
    assert_eq!(snapshot.size.columns, 40);
    assert_eq!(snapshot.size.lines, 8);
    assert!(snapshot.visible_text().contains("MODEL_HOST_READY"));
    host.shutdown();
}
