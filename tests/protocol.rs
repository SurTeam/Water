use std::io::Cursor;

use water::app::model::StateDump;
use water::command::{
    AppCommand, PaneCommand, SplitDirection, TabCommand, TerminalCommand, WorkspaceCommand,
};
use water::control::protocol::{PROTOCOL_VERSION, RpcMethod, RpcRequest, read_frame, write_frame};
use water::terminal::{default_shell_args, default_shell_program};

#[test]
fn state_dump_deserialization_migrates_legacy_and_new_shapes() {
    let legacy: StateDump = serde_json::from_value(serde_json::json!({
        "state_revision": 7,
        "workspace": {
            "id": 42,
            "title": "Legacy",
            "active_tab": null,
            "tabs": []
        },
        "focused_pane": null
    }))
    .expect("legacy state dump decodes");
    assert_eq!(legacy.active_workspace, Some(42.into()));
    assert_eq!(legacy.workspaces.len(), 1);
    assert_eq!(legacy.workspace.as_ref().unwrap().id, 42.into());

    let current: StateDump = serde_json::from_value(serde_json::json!({
        "state_revision": 8,
        "workspaces": [{
            "id": 99,
            "title": "Current",
            "active_tab": null,
            "tabs": []
        }],
        "active_workspace": 99,
        "focused_pane": null
    }))
    .expect("current state dump without compatibility alias decodes");
    assert_eq!(current.workspace.as_ref().unwrap().id, 99.into());
    assert_eq!(current.workspaces.len(), 1);
}

#[test]
fn commands_use_stable_dot_named_wire_types() {
    let command = AppCommand::Pane(PaneCommand::Split {
        pane_id: None,
        direction: SplitDirection::Right,
    });
    let value = serde_json::to_value(command).expect("command serializes");
    assert_eq!(value["type"], "pane.split");
    assert_eq!(value["direction"], "right");
}

#[test]
fn pane_agent_rename_uses_a_stable_wire_type() {
    let command = AppCommand::Pane(PaneCommand::RenameAgent {
        pane_id: Some(42.into()),
        label: "Build Bot".to_owned(),
    });
    let value = serde_json::to_value(&command).expect("agent rename serializes");
    assert_eq!(value["type"], "pane.agent_rename");
    assert_eq!(value["pane_id"], 42);
    assert_eq!(value["label"], "Build Bot");
    let decoded: AppCommand = serde_json::from_value(value).expect("agent rename decodes");
    assert_eq!(decoded, command);
}

#[test]
fn pane_resize_split_uses_a_stable_wire_type() {
    let command = AppCommand::Pane(PaneCommand::ResizeSplit {
        tab_id: 7.into(),
        path: vec![false, true],
        ratio: 0.42,
    });
    let value = serde_json::to_value(&command).expect("resize split serializes");
    assert_eq!(value["type"], "pane.resize_split");
    assert_eq!(value["tab_id"], 7);
    assert_eq!(value["path"], serde_json::json!([false, true]));
    let decoded: AppCommand = serde_json::from_value(value).expect("resize split decodes");
    assert_eq!(decoded, command);
}

#[test]
fn workspace_create_and_new_keep_stable_wire_types() {
    let create = AppCommand::Workspace(WorkspaceCommand::Create);
    let create_value = serde_json::to_value(&create).expect("workspace create serializes");
    assert_eq!(create_value["type"], "workspace.create");
    assert_eq!(
        serde_json::from_value::<AppCommand>(create_value).unwrap(),
        create
    );

    let new = AppCommand::Workspace(WorkspaceCommand::New);
    let new_value = serde_json::to_value(&new).expect("workspace new serializes");
    assert_eq!(new_value["type"], "workspace.new");
    assert_eq!(
        serde_json::from_value::<AppCommand>(new_value).unwrap(),
        new
    );
}

#[test]
fn tab_new_in_workspace_uses_a_stable_wire_type() {
    let command = AppCommand::Tab(TabCommand::NewInWorkspace {
        workspace_id: 42.into(),
        title: Some("Build".to_owned()),
    });
    let value = serde_json::to_value(&command).expect("targeted tab command serializes");
    assert_eq!(value["type"], "tab.new_in_workspace");
    assert_eq!(value["workspace_id"], 42);
    let decoded: AppCommand = serde_json::from_value(value).expect("targeted tab command decodes");
    assert_eq!(decoded, command);
}

#[test]
fn workspace_reorder_uses_a_stable_wire_type() {
    let command = AppCommand::Workspace(WorkspaceCommand::Reorder {
        workspace_id: Some(42.into()),
        index: 3,
    });
    let value = serde_json::to_value(&command).expect("workspace reorder serializes");
    assert_eq!(value["type"], "workspace.reorder");
    assert_eq!(value["workspace_id"], 42);
    assert_eq!(value["index"], 3);
    let decoded: AppCommand = serde_json::from_value(value).expect("workspace reorder decodes");
    assert_eq!(decoded, command);
}

#[test]
fn pane_move_to_workspace_uses_a_stable_wire_type() {
    let command = AppCommand::Pane(PaneCommand::MoveToWorkspace {
        pane_id: Some(7.into()),
        workspace_id: 42.into(),
    });
    let value = serde_json::to_value(&command).expect("pane move serializes");
    assert_eq!(value["type"], "pane.move_to_workspace");
    assert_eq!(value["pane_id"], 7);
    assert_eq!(value["workspace_id"], 42);
    let decoded: AppCommand = serde_json::from_value(value).expect("pane move decodes");
    assert_eq!(decoded, command);
}

#[test]
fn terminal_commands_use_stable_wire_types() {
    let command = AppCommand::Terminal(TerminalCommand::Spawn {
        pane_id: None,
        program: "/bin/sh".to_owned(),
        args: vec!["-c".to_owned(), "printf ready".to_owned()],
        columns: 80,
        lines: 24,
    });
    let value = serde_json::to_value(&command).expect("terminal command serializes");
    assert_eq!(value["type"], "terminal.spawn");
    assert_eq!(value["program"], "/bin/sh");
    let decoded: AppCommand = serde_json::from_value(value).expect("terminal command decodes");
    assert_eq!(decoded, command);
}

#[test]
fn terminal_byte_commands_round_trip_without_utf8_reencoding() {
    let command = AppCommand::Terminal(TerminalCommand::SendBytes {
        terminal_id: Some(7.into()),
        pane_id: None,
        bytes: vec![0x1b, b'[', b'M', 0xff],
    });
    let value = serde_json::to_value(&command).expect("byte command serializes");
    assert_eq!(value["type"], "terminal.send_bytes");
    let decoded: AppCommand = serde_json::from_value(value).expect("byte command decodes");
    assert_eq!(decoded, command);
}

#[test]
fn terminal_spawn_defaults_to_the_detected_real_shell() {
    let command: AppCommand = serde_json::from_value(serde_json::json!({
        "type": "terminal.spawn"
    }))
    .expect("terminal command with defaults decodes");
    match command {
        AppCommand::Terminal(TerminalCommand::Spawn {
            program,
            args,
            columns,
            lines,
            ..
        }) => {
            assert_eq!(program, default_shell_program());
            assert_eq!(args, default_shell_args(&program));
            assert_eq!(columns, 80);
            assert_eq!(lines, 24);
        }
        command => panic!("unexpected command: {command:?}"),
    }
}

#[test]
fn ui_automation_methods_use_stable_wire_names() {
    let request = RpcRequest {
        protocol_version: PROTOCOL_VERSION,
        request_id: 8,
        method: RpcMethod::UiKeystroke {
            keystroke: "cmd-m".to_owned(),
        },
    };
    let value = serde_json::to_value(&request).expect("UI request serializes");
    assert_eq!(value["method"], "ui.keystroke");
    assert_eq!(value["params"]["keystroke"], "cmd-m");
    let decoded: RpcRequest = serde_json::from_value(value).expect("UI request decodes");
    assert!(matches!(
        decoded.method,
        RpcMethod::UiKeystroke { keystroke } if keystroke == "cmd-m"
    ));
}

#[test]
fn ui_screenshot_uses_a_stable_wire_name() {
    let request = RpcRequest {
        protocol_version: PROTOCOL_VERSION,
        request_id: 9,
        method: RpcMethod::UiScreenshot {
            path: "/tmp/water.png".to_owned(),
        },
    };
    let value = serde_json::to_value(&request).expect("screenshot request serializes");
    assert_eq!(value["method"], "ui.screenshot");
    assert_eq!(value["params"]["path"], "/tmp/water.png");
    let decoded: RpcRequest = serde_json::from_value(value).expect("screenshot request decodes");
    assert!(matches!(
        decoded.method,
        RpcMethod::UiScreenshot { path } if path == "/tmp/water.png"
    ));
}

#[test]
fn ui_wheel_uses_a_stable_wire_name() {
    let request = RpcRequest {
        protocol_version: PROTOCOL_VERSION,
        request_id: 10,
        method: RpcMethod::UiWheel {
            x: 120.0,
            y: 20.0,
            dx: 0.0,
            dy: 3.0,
        },
    };
    let value = serde_json::to_value(&request).expect("wheel request serializes");
    assert_eq!(value["method"], "ui.wheel");
    assert_eq!(value["params"]["dy"], 3.0);
    let decoded: RpcRequest = serde_json::from_value(value).expect("wheel request decodes");
    assert!(matches!(
        decoded.method,
        RpcMethod::UiWheel { dy, .. } if (dy - 3.0).abs() < f32::EPSILON
    ));
}

#[test]
fn length_prefixed_protocol_round_trips() {
    let request = RpcRequest {
        protocol_version: PROTOCOL_VERSION,
        request_id: 7,
        method: RpcMethod::StateDump,
    };
    let mut bytes = Vec::new();
    write_frame(&mut bytes, &request).expect("frame writes");
    let decoded: RpcRequest = read_frame(&mut Cursor::new(bytes)).expect("frame reads");
    assert_eq!(decoded.request_id, 7);
    assert_eq!(decoded.protocol_version, PROTOCOL_VERSION);
}
