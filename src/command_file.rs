//! Finder command files are processed off the UI thread through the command transport.
use std::path::PathBuf;
use std::sync::{Arc, mpsc};

use anyhow::{Context, Result, bail};

use crate::app::CommandTransport;
use crate::command::{
    AppCommand, OperationResult, OperationStatus, TabCommand, TerminalCommand, WorkspaceCommand,
};
use crate::config::AppConfig;

pub fn start_opener(
    client: Arc<dyn CommandTransport>,
    config: AppConfig,
) -> Result<mpsc::SyncSender<String>> {
    let (sender, receiver) = mpsc::sync_channel::<String>(32);
    std::thread::Builder::new()
        .name("water-command-files".into())
        .spawn(move || {
            for url in receiver {
                if let Err(error) = open_command_file(client.as_ref(), &config, &url) {
                    tracing::warn!(target: "water::workspace", ?error, "could not open command file");
                }
            }
        })?;
    Ok(sender)
}

fn command_path(value: &str) -> Result<PathBuf> {
    let url = url::Url::parse(value).context("invalid command file URL")?;
    let path = url
        .to_file_path()
        .map_err(|_| anyhow::anyhow!("expected a local file URL"))?;
    if path
        .extension()
        .is_none_or(|extension| extension != "command")
        || !path.is_file()
    {
        bail!("expected an existing .command file");
    }
    Ok(path)
}

fn execute(client: &dyn CommandTransport, command: AppCommand) -> Result<OperationResult> {
    let id = client.dispatch(command)?;
    let operation = client.wait_operation(id)?;
    if operation.status != OperationStatus::Succeeded {
        bail!("command file operation failed: {:?}", operation.error);
    }
    operation
        .result
        .context("command file operation returned no result")
}

pub fn open_command_file(
    client: &dyn CommandTransport,
    config: &AppConfig,
    value: &str,
) -> Result<()> {
    let path = command_path(value)?;
    // A dedicated workspace avoids racing the user's current workspace selection.
    let OperationResult::WorkspaceCreated { workspace_id } =
        execute(client, AppCommand::Workspace(WorkspaceCommand::Create))?
    else {
        bail!("expected a new workspace")
    };
    let OperationResult::TabCreated { tab_id } = execute(
        client,
        AppCommand::Tab(TabCommand::NewInWorkspace {
            workspace_id,
            title: path
                .file_name()
                .map(|name| name.to_string_lossy().into_owned()),
        }),
    )?
    else {
        bail!("expected a new tab")
    };
    let state = client.state_dump()?;
    let pane_id = state
        .workspaces
        .iter()
        .flat_map(|workspace| &workspace.tabs)
        .find(|tab| tab.id == tab_id)
        .context("new command tab disappeared")?
        .active_pane;
    // Pass paths as positional arguments, never shell source. Execute the file
    // normally (respecting its shebang and permissions), then retain a real shell.
    let mut args = vec![
        "-c".into(),
        "cd -- \"$1\" && \"$2\"; shift 2; exec \"$@\"".into(),
        "water-command".into(),
        path.parent()
            .context("command file has no parent")?
            .to_string_lossy()
            .into_owned(),
        path.to_str()
            .context("command file path is not UTF-8")?
            .into(),
        config.shell.program.clone(),
    ];
    args.extend(config.shell.args.clone());
    execute(
        client,
        AppCommand::Terminal(TerminalCommand::Spawn {
            pane_id: Some(pane_id),
            program: "/bin/sh".into(),
            args,
            columns: config.terminal.default_columns,
            lines: config.terminal.default_lines,
        }),
    )?;
    Ok(())
}
