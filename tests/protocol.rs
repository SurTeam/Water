use std::io::Cursor;

use water::command::{AppCommand, PaneCommand, SplitDirection, TerminalCommand};
use water::control::protocol::{PROTOCOL_VERSION, RpcMethod, RpcRequest, read_frame, write_frame};
use water::terminal::{default_shell_args, default_shell_program};

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
