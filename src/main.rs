use std::path::PathBuf;

use anyhow::{Context as _, Result, bail};
use gpui::{App, AppContext, Bounds, Focusable, WindowBounds, WindowOptions, px, size};
use gpui_platform::application as platform_application;

use water::app::{CommandClient, ModelHost};
use water::command::{AppCommand, OperationStatus, TabCommand, WorkspaceCommand};
use water::config::AppConfig;
use water::control::{ControlServer, default_socket_path};

use water::ui::{WorkspaceView, spawn_snapshot_listener};

fn main() -> Result<()> {
    init_tracing();
    let startup = parse_startup_options(std::env::args().skip(1))?;
    let config = AppConfig::load_from_path(&startup.config_path)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    tracing::info!(
        target: "water::workspace",
        socket = %startup.socket_path.display(),
        config = %startup.config_path.display(),
        scrollback_lines = config.terminal.scrollback_lines,
        initial_workspace = startup.initial_workspace,
        initial_terminal = startup.initial_terminal,
        "starting water"
    );

    let mut model_host = ModelHost::start_with_config(config.clone());
    let client = model_host.client();
    if startup.initial_workspace {
        dispatch_checked(&client, AppCommand::Workspace(WorkspaceCommand::Create))?;
    }
    if startup.initial_terminal {
        dispatch_checked(&client, AppCommand::Tab(TabCommand::New { title: None }))?;
    }
    let initial_snapshot = client
        .state_dump()
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let snapshot_receiver = model_host.take_snapshot_receiver();
    let mut control_server = ControlServer::start(startup.socket_path, client.clone())
        .context("failed to start control socket")?;

    platform_application().run(move |cx: &mut App| {
        let root = cx.new(|cx| {
            WorkspaceView::new_with_config(
                client.clone(),
                initial_snapshot.clone(),
                cx.focus_handle(),
                config.clone(),
            )
        });
        let focus_handle = root.read(cx).focus_handle(cx);
        spawn_snapshot_listener(cx, root.clone(), snapshot_receiver).detach();
        let bounds = Bounds::centered(None, size(px(1100.), px(760.)), cx);
        cx.open_window(
            WindowOptions {
                window_bounds: Some(WindowBounds::Windowed(bounds)),
                ..Default::default()
            },
            move |window, cx| {
                window.focus(&focus_handle, cx);
                root
            },
        )
        .expect("failed to open water window");
        cx.activate(true);
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
    socket_path: PathBuf,
    config_path: PathBuf,
    initial_workspace: bool,
    initial_terminal: bool,
}

fn parse_startup_options(mut args: impl Iterator<Item = String>) -> Result<StartupOptions> {
    let mut socket_path = default_socket_path();
    let mut config_path = std::env::var_os("WATER_CONFIG")
        .map(PathBuf::from)
        .unwrap_or_else(AppConfig::default_path);
    let mut initial_workspace = true;
    let mut initial_terminal = true;
    while let Some(argument) = args.next() {
        if argument == "--control-socket" {
            socket_path = args
                .next()
                .context("--control-socket requires a path")?
                .into();
        } else if let Some(path) = argument.strip_prefix("--control-socket=") {
            socket_path = path.into();
        } else if argument == "--config" {
            config_path = args.next().context("--config requires a path")?.into();
        } else if let Some(path) = argument.strip_prefix("--config=") {
            config_path = path.into();
        } else if argument == "--no-initial-terminal" {
            initial_terminal = false;
        } else if argument == "--empty-workspace" || argument == "--no-initial-workspace" {
            initial_workspace = false;
            initial_terminal = false;
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
