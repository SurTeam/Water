use std::path::PathBuf;

use anyhow::{Context, Result, bail};
use serde::Serialize;

use water::automation::{ControlBackend, Scenario, ScenarioRunner};
use water::command::{
    AppCommand, FocusDirection, OperationStatus, PaneCommand, SplitDirection, SurfaceCommand,
    TabCommand, TerminalCommand, WorkspaceCommand,
};
use water::control::{ControlClient, default_socket_path};
use water::ids::{PaneId, TabId, TerminalId};
use water::surface::SurfaceKind;
use water::terminal::{default_shell_args, default_shell_program};

fn main() -> Result<()> {
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    let (socket_path, arguments) = extract_socket(arguments)?;
    let client = ControlClient::new(socket_path);
    if arguments.is_empty() {
        print_usage();
        bail!("a command is required");
    }

    match arguments[0].as_str() {
        "ping" => {
            client.ping().context("ping failed")?;
            println!("pong");
        }
        "state" => print_json(&client.state_dump().context("state request failed")?)?,
        "debug" => run_debug(&client, &arguments[1..])?,
        "workspace" => run_workspace(&client, &arguments[1..])?,
        "tab" => run_tab(&client, &arguments[1..])?,
        "pane" => run_pane(&client, &arguments[1..])?,
        "surface" => run_surface(&client, &arguments[1..])?,
        "terminal" => run_terminal(&client, &arguments[1..])?,
        "operation" => run_operation(&client, &arguments[1..])?,
        "scenario" => run_scenario(&client, &arguments[1..])?,
        "--help" | "-h" => print_usage(),
        command => bail!("unknown command: {command}"),
    }
    Ok(())
}

fn run_debug(client: &ControlClient, arguments: &[String]) -> Result<()> {
    match arguments.first().map(String::as_str) {
        Some("memory") => print_json(&client.memory_stats().context("memory request failed")?)?,
        Some(command) => bail!("unknown debug command: {command}"),
        None => bail!("debug requires a command"),
    }
    Ok(())
}

fn run_workspace(client: &ControlClient, arguments: &[String]) -> Result<()> {
    match arguments.first().map(String::as_str) {
        Some("create") | Some("new") => {
            dispatch_and_print(client, AppCommand::Workspace(WorkspaceCommand::Create))?
        }
        Some("list") => {
            let state = client.state_dump().context("state request failed")?;
            print_json(&state.workspace)?;
        }
        Some(command) => bail!("unknown workspace command: {command}"),
        None => bail!("workspace requires a command"),
    }
    Ok(())
}

fn run_tab(client: &ControlClient, arguments: &[String]) -> Result<()> {
    let Some(command) = arguments.first().map(String::as_str) else {
        bail!("tab requires a command")
    };
    match command {
        "new" => {
            let title = optional_value(arguments, "--title")?;
            dispatch_and_print(client, AppCommand::Tab(TabCommand::New { title }))?;
        }
        "close" => {
            let tab_id = optional_id::<TabId>(arguments, "--tab")?
                .or_else(|| bare_id::<TabId>(&arguments[1..]).ok());
            dispatch_and_print(client, AppCommand::Tab(TabCommand::Close { tab_id }))?;
        }
        "activate" => {
            let tab_id = optional_id::<TabId>(arguments, "--tab")?
                .or_else(|| bare_id::<TabId>(&arguments[1..]).ok());
            let index = optional_usize(arguments, "--index")?;
            if tab_id.is_none() && index.is_none() {
                bail!("tab activate requires an ID or --index")
            }
            dispatch_and_print(
                client,
                AppCommand::Tab(TabCommand::Activate { tab_id, index }),
            )?;
        }
        _ => bail!("unknown tab command: {command}"),
    }
    Ok(())
}

fn run_pane(client: &ControlClient, arguments: &[String]) -> Result<()> {
    let Some(command) = arguments.first().map(String::as_str) else {
        bail!("pane requires a command")
    };
    match command {
        "split" => {
            let direction = parse_split_direction(arguments)?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            dispatch_and_print(
                client,
                AppCommand::Pane(PaneCommand::Split { pane_id, direction }),
            )?;
        }
        "close" => {
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?
                .or_else(|| bare_id::<PaneId>(&arguments[1..]).ok());
            dispatch_and_print(client, AppCommand::Pane(PaneCommand::Close { pane_id }))?;
        }
        "focus" => {
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?
                .or_else(|| bare_id::<PaneId>(&arguments[1..]).ok());
            let direction = parse_focus_direction(arguments)?;
            if pane_id.is_none() && direction.is_none() {
                bail!("pane focus requires an ID or direction")
            }
            dispatch_and_print(
                client,
                AppCommand::Pane(PaneCommand::Focus { pane_id, direction }),
            )?;
        }
        "resize" => {
            let ratio = required_f32(arguments, "--ratio")?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            dispatch_and_print(
                client,
                AppCommand::Pane(PaneCommand::Resize { pane_id, ratio }),
            )?;
        }
        _ => bail!("unknown pane command: {command}"),
    }
    Ok(())
}

fn run_surface(client: &ControlClient, arguments: &[String]) -> Result<()> {
    match arguments.first().map(String::as_str) {
        Some("replace") => {
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let kind = if arguments.iter().any(|argument| argument == "--empty") {
                SurfaceKind::Empty
            } else {
                bail!("Phase 1 only supports --empty")
            };
            dispatch_and_print(
                client,
                AppCommand::Surface(SurfaceCommand::Replace { pane_id, kind }),
            )?;
        }
        Some(command) => bail!("unknown surface command: {command}"),
        None => bail!("surface requires a command"),
    }
    Ok(())
}

fn run_terminal(client: &ControlClient, arguments: &[String]) -> Result<()> {
    let Some(command) = arguments.first().map(String::as_str) else {
        bail!(
            "terminal requires spawn, send, send-bytes, resize, scroll, contains, wait-exit, or snapshot"
        )
    };
    match command {
        "spawn" => {
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let columns = optional_usize(arguments, "--columns")?.unwrap_or(80);
            let lines = optional_usize(arguments, "--lines")?.unwrap_or(24);
            let explicit_program = optional_value(arguments, "--program")?;
            let positional = terminal_positional(arguments);
            let program = explicit_program
                .clone()
                .or_else(|| positional.first().map(|value| (*value).to_owned()))
                .unwrap_or_else(default_shell_program);
            let mut args: Vec<String> = if explicit_program.is_some() {
                positional.into_iter().map(str::to_owned).collect()
            } else {
                positional.into_iter().skip(1).map(str::to_owned).collect()
            };
            if args.is_empty() {
                args = default_shell_args(&program);
            }
            dispatch_and_print(
                client,
                AppCommand::Terminal(TerminalCommand::Spawn {
                    pane_id,
                    program,
                    args,
                    columns,
                    lines,
                }),
            )?;
        }
        "send" | "send-text" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let text = optional_value(arguments, "--text")?.unwrap_or_else(|| {
                terminal_positional(arguments)
                    .into_iter()
                    .collect::<Vec<_>>()
                    .join(" ")
            });
            if text.is_empty() {
                bail!("terminal send requires text")
            }
            dispatch_and_print(
                client,
                AppCommand::Terminal(TerminalCommand::SendText {
                    terminal_id,
                    pane_id,
                    text,
                }),
            )?;
        }
        "send-bytes" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let hex = optional_value(arguments, "--hex")?.unwrap_or_else(|| {
                terminal_positional(arguments)
                    .into_iter()
                    .collect::<Vec<_>>()
                    .join("")
            });
            let bytes = parse_hex_bytes(&hex)?;
            if bytes.is_empty() {
                bail!("terminal send-bytes requires hexadecimal bytes")
            }
            dispatch_and_print(
                client,
                AppCommand::Terminal(TerminalCommand::SendBytes {
                    terminal_id,
                    pane_id,
                    bytes,
                }),
            )?;
        }
        "resize" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let columns = required_usize(arguments, "--columns")?;
            let lines = required_usize(arguments, "--lines")?;
            dispatch_and_print(
                client,
                AppCommand::Terminal(TerminalCommand::Resize {
                    terminal_id,
                    pane_id,
                    columns,
                    lines,
                }),
            )?;
        }
        "scroll" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?;
            let pane_id = optional_id::<PaneId>(arguments, "--pane")?;
            let lines = required_i32(arguments, "--lines")?;
            dispatch_and_print(
                client,
                AppCommand::Terminal(TerminalCommand::Scroll {
                    terminal_id,
                    pane_id,
                    lines,
                }),
            )?;
        }
        "contains" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?
                .context("terminal contains requires --terminal")?;
            let timeout_ms = optional_usize(arguments, "--timeout-ms")?.unwrap_or(5_000) as u64;
            let text = optional_value(arguments, "--text")?.unwrap_or_else(|| {
                terminal_positional(arguments)
                    .into_iter()
                    .collect::<Vec<_>>()
                    .join(" ")
            });
            let snapshot = client
                .terminal_contains(
                    terminal_id,
                    text,
                    std::time::Duration::from_millis(timeout_ms),
                )
                .context("terminal contains failed")?;
            print_json(&snapshot)?;
        }
        "wait-exit" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?
                .context("terminal wait-exit requires --terminal")?;
            let timeout_ms = optional_usize(arguments, "--timeout-ms")?.unwrap_or(5_000) as u64;
            let snapshot = client
                .wait_terminal_exit(terminal_id, std::time::Duration::from_millis(timeout_ms))
                .context("terminal wait-exit failed")?;
            print_json(&snapshot)?;
        }
        "snapshot" => {
            let terminal_id = optional_id::<TerminalId>(arguments, "--terminal")?
                .context("terminal snapshot requires --terminal")?;
            print_json(&client.terminal_snapshot(terminal_id)?)?;
        }
        _ => bail!("unknown terminal command: {command}"),
    }
    Ok(())
}

fn terminal_positional(arguments: &[String]) -> Vec<&str> {
    let mut values = Vec::new();
    let mut index = 1;
    while index < arguments.len() {
        match arguments[index].as_str() {
            "--pane" | "--terminal" | "--columns" | "--lines" | "--timeout-ms" | "--program"
            | "--text" | "--hex" => index += 2,
            _ => {
                values.push(arguments[index].as_str());
                index += 1;
            }
        }
    }
    values
}

fn required_usize(arguments: &[String], flag: &str) -> Result<usize> {
    optional_usize(arguments, flag)?.context(format!("{flag} is required"))
}

fn required_i32(arguments: &[String], flag: &str) -> Result<i32> {
    optional_value(arguments, flag)?
        .context(format!("{flag} is required"))?
        .parse()
        .with_context(|| format!("invalid value for {flag}"))
}

fn run_operation(client: &ControlClient, arguments: &[String]) -> Result<()> {
    let Some(command) = arguments.first().map(String::as_str) else {
        bail!("operation requires get or wait")
    };
    let operation_id = arguments
        .get(1)
        .context("operation ID is required")?
        .parse::<u64>()
        .map(water::ids::OperationId::new)
        .context("invalid operation ID")?;
    let operation = match command {
        "get" => client
            .get_operation(operation_id)
            .context("operation request failed")?,
        "wait" => Some(
            client
                .wait_operation(operation_id)
                .context("operation wait failed")?,
        ),
        _ => bail!("unknown operation command: {command}"),
    };
    print_json(&operation)?;
    Ok(())
}

fn run_scenario(client: &ControlClient, arguments: &[String]) -> Result<()> {
    if arguments.first().map(String::as_str) != Some("run") {
        bail!("scenario requires: scenario run PATH")
    }
    let path = arguments.get(1).context("scenario path is required")?;
    let scenario = Scenario::from_path(path).map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let mut runner = ScenarioRunner::new(ControlBackend::new(client.clone()));
    runner
        .run(&scenario)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    println!("scenario {}: passed", scenario.name);
    Ok(())
}

fn dispatch_and_print(client: &ControlClient, command: AppCommand) -> Result<()> {
    let operation_id = client.dispatch(command).context("dispatch failed")?;
    let operation = client
        .wait_operation(operation_id)
        .context("operation wait failed")?;
    if operation.status == OperationStatus::Failed {
        let error = operation
            .error
            .map(|error| format!("{}: {}", error.code, error.message))
            .unwrap_or_else(|| "operation failed".to_owned());
        bail!(error);
    }
    print_json(&operation)
}

fn extract_socket(arguments: Vec<String>) -> Result<(PathBuf, Vec<String>)> {
    let mut socket_path = default_socket_path();
    let mut filtered = Vec::with_capacity(arguments.len());
    let mut index = 0;
    while index < arguments.len() {
        if arguments[index] == "--socket" || arguments[index] == "--control-socket" {
            socket_path = arguments
                .get(index + 1)
                .context("--socket requires a path")?
                .into();
            index += 2;
        } else if let Some(path) = arguments[index]
            .strip_prefix("--socket=")
            .or_else(|| arguments[index].strip_prefix("--control-socket="))
        {
            socket_path = path.into();
            index += 1;
        } else {
            filtered.push(arguments[index].clone());
            index += 1;
        }
    }
    Ok((socket_path, filtered))
}

fn optional_value(arguments: &[String], flag: &str) -> Result<Option<String>> {
    let Some(index) = arguments.iter().position(|argument| argument == flag) else {
        return Ok(None);
    };
    Ok(Some(
        arguments
            .get(index + 1)
            .with_context(|| format!("{flag} requires a value"))?
            .clone(),
    ))
}

fn optional_id<T>(arguments: &[String], flag: &str) -> Result<Option<T>>
where
    T: From<u64>,
{
    let Some(index) = arguments.iter().position(|argument| argument == flag) else {
        return Ok(None);
    };
    let value = arguments
        .get(index + 1)
        .with_context(|| format!("{flag} requires an ID"))?
        .parse::<u64>()
        .with_context(|| format!("invalid ID for {flag}"))?;
    Ok(Some(T::from(value)))
}

fn optional_usize(arguments: &[String], flag: &str) -> Result<Option<usize>> {
    let Some(index) = arguments.iter().position(|argument| argument == flag) else {
        return Ok(None);
    };
    Ok(Some(
        arguments
            .get(index + 1)
            .with_context(|| format!("{flag} requires a value"))?
            .parse()
            .with_context(|| format!("invalid value for {flag}"))?,
    ))
}

fn required_f32(arguments: &[String], flag: &str) -> Result<f32> {
    optional_value(arguments, flag)?
        .context(format!("{flag} is required"))?
        .parse()
        .with_context(|| format!("invalid value for {flag}"))
}

fn bare_id<T>(arguments: &[String]) -> Result<T>
where
    T: From<u64>,
{
    let value = arguments
        .iter()
        .find(|argument| !argument.starts_with('-'))
        .context("ID is required")?
        .parse::<u64>()
        .context("invalid ID")?;
    Ok(T::from(value))
}

fn parse_split_direction(arguments: &[String]) -> Result<SplitDirection> {
    if arguments.iter().any(|argument| argument == "--left") {
        Ok(SplitDirection::Left)
    } else if arguments.iter().any(|argument| argument == "--right") {
        Ok(SplitDirection::Right)
    } else if arguments.iter().any(|argument| argument == "--up") {
        Ok(SplitDirection::Up)
    } else if arguments.iter().any(|argument| argument == "--down") {
        Ok(SplitDirection::Down)
    } else {
        bail!("pane split requires --left, --right, --up, or --down")
    }
}

fn parse_focus_direction(arguments: &[String]) -> Result<Option<FocusDirection>> {
    Ok(if arguments.iter().any(|argument| argument == "--left") {
        Some(FocusDirection::Left)
    } else if arguments.iter().any(|argument| argument == "--right") {
        Some(FocusDirection::Right)
    } else if arguments.iter().any(|argument| argument == "--up") {
        Some(FocusDirection::Up)
    } else if arguments.iter().any(|argument| argument == "--down") {
        Some(FocusDirection::Down)
    } else {
        None
    })
}

fn parse_hex_bytes(value: &str) -> Result<Vec<u8>> {
    let digits: Vec<char> = value
        .chars()
        .filter(|character| !character.is_ascii_whitespace() && *character != ':')
        .collect();
    if !digits.len().is_multiple_of(2) {
        bail!("hex byte input must contain pairs of digits")
    }
    let mut bytes = Vec::with_capacity(digits.len() / 2);
    for index in (0..digits.len()).step_by(2) {
        let high = hex_digit(digits[index])?;
        let low = hex_digit(digits[index + 1])?;
        bytes.push((high << 4) | low);
    }
    Ok(bytes)
}

fn hex_digit(character: char) -> Result<u8> {
    character
        .to_digit(16)
        .map(|value| value as u8)
        .with_context(|| format!("invalid hexadecimal digit: {character}"))
}

fn print_json<T: Serialize>(value: &T) -> Result<()> {
    println!("{}", serde_json::to_string_pretty(value)?);
    Ok(())
}

fn print_usage() {
    println!(
        "waterctl [--socket PATH] <state|workspace|tab|pane|surface|terminal|operation|scenario|debug> ...\n\n\
         Examples:\n\
           waterctl state\n\
           waterctl tab new --title Main\n\
           waterctl pane split --right\n\
           waterctl pane focus 3\n\
           waterctl operation wait 7\n\
           waterctl scenario run tests/scenarios/workspace_basic.json"
    );
}

// The CLI uses one numeric syntax for IDs. The command's typed field supplies
// the final type at construction, so parsing remains small without a generic
// ID parser or a second scripting language.
