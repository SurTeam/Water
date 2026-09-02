use std::path::PathBuf;

use anyhow::{Context as _, Result, bail};
use gpui::App;
use gpui_platform::application as platform_application;

use water::app::{CommandClient, ModelHost};
use water::command::{AppCommand, OperationStatus, TabCommand, WorkspaceCommand};
use water::config::AppConfig;
use water::control::{ControlServer, default_socket_path};

use water::ui::{WaterApplication, ui_control_channel};

fn main() -> Result<()> {
    init_tracing();
    let startup = parse_startup_options(std::env::args().skip(1))?;
    let config = AppConfig::load_from_path(&startup.config_path)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let socket_path = startup
        .socket_path
        .clone()
        .or_else(|| std::env::var_os("WATER_CONTROL_SOCKET").map(PathBuf::from))
        .or_else(|| config.startup.control_socket.clone().map(PathBuf::from))
        .unwrap_or_else(default_socket_path);
    tracing::info!(
        target: "water::workspace",
        socket = %socket_path.display(),
        config = %startup.config_path.display(),
        scrollback_lines = config.terminal.scrollback_lines,
        initial_workspace = startup.initial_workspace,
        initial_terminal = startup.initial_terminal,
        "starting water"
    );

    let mut model_host = ModelHost::start_with_config(config.clone());
    let client = model_host.client();
    let initial_workspace = startup
        .initial_workspace
        .unwrap_or(config.startup.initial_workspace);
    let initial_terminal = startup
        .initial_terminal
        .unwrap_or(config.startup.initial_terminal)
        && initial_workspace;
    if initial_workspace {
        dispatch_checked(&client, AppCommand::Workspace(WorkspaceCommand::Create))?;
    }
    if initial_terminal {
        dispatch_checked(&client, AppCommand::Tab(TabCommand::New { title: None }))?;
    }
    let initial_snapshot = client
        .state_dump()
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let snapshot_receiver = model_host.take_snapshot_receiver();
    let (ui_control_client, ui_control_receiver) = ui_control_channel();
    let mut control_server =
        ControlServer::start_with_ui(socket_path, client.clone(), ui_control_client)
            .context("failed to start control socket")?;
    let ui_application = WaterApplication::new_with_config_path(
        client,
        initial_snapshot,
        config,
        startup.config_path.clone(),
    );
    let reopen_application = ui_application.clone();
    let application =
        platform_application().with_restart_arguments(std::env::args_os().skip(1).collect());
    application.on_reopen(move |cx| reopen_application.reopen(cx));
    application.run(move |cx: &mut App| {
        ui_application.install(cx, snapshot_receiver, ui_control_receiver);
    });

    control_server.shutdown();
    model_host.shutdown();
    Ok(())
}

fn dispatch_checked(client: &CommandClient, command: AppCommand) -> Result<()> {
    let operation_id = client
        .dispatch(command)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let operation = client
        .wait_operation(operation_id)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    if operation.status == OperationStatus::Failed {
        let error = operation
            .error
            .map(|error| format!("{}: {}", error.code, error.message))
            .unwrap_or_else(|| "unknown command failure".to_owned());
        bail!(error);
    }
    Ok(())
}

struct StartupOptions {
    socket_path: Option<PathBuf>,
    config_path: PathBuf,
    initial_workspace: Option<bool>,
    initial_terminal: Option<bool>,
}

fn parse_startup_options(mut args: impl Iterator<Item = String>) -> Result<StartupOptions> {
    let mut socket_path = None;
    let mut config_path = std::env::var_os("WATER_CONFIG")
        .map(PathBuf::from)
        .unwrap_or_else(AppConfig::default_load_path);
    let mut initial_workspace = None;
    let mut initial_terminal = None;
    while let Some(argument) = args.next() {
        if argument == "--control-socket" {
            socket_path = Some(
                args.next()
                    .context("--control-socket requires a path")?
                    .into(),
            );
        } else if let Some(path) = argument.strip_prefix("--control-socket=") {
            socket_path = Some(path.into());
        } else if argument == "--config" {
            config_path = args.next().context("--config requires a path")?.into();
        } else if let Some(path) = argument.strip_prefix("--config=") {
            config_path = path.into();
        } else if argument == "--no-initial-terminal" {
            initial_terminal = Some(false);
        } else if argument == "--empty-workspace" || argument == "--no-initial-workspace" {
            initial_workspace = Some(false);
            initial_terminal = Some(false);
        } else if argument == "--help" || argument == "-h" {
            println!(
                "water [--control-socket PATH] [--config PATH] [--no-initial-terminal] [--empty-workspace]"
            );
            std::process::exit(0);
        } else {
            bail!("unknown argument: {argument}");
        }
    }
    Ok(StartupOptions {
        socket_path,
        config_path,
        initial_workspace,
        initial_terminal,
    })
}

fn init_tracing() {
    let filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("water=info"));
    let _ = tracing_subscriber::fmt().with_env_filter(filter).try_init();
}
