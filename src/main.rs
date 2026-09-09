use std::path::{Path, PathBuf};
use std::time::Duration;

use anyhow::{Context as _, Result, bail};
use gpui::App;
use gpui_platform::application as platform_application;

use water::app::{CommandTransport, ModelHost};
use water::command::{AppCommand, OperationStatus, TabCommand, WorkspaceCommand};
use water::config::AppConfig;
use water::control::{
    ControlClient, ControlServer, RemoteCommandClient, connect_water_session, default_socket_path,
    spawn_state_polling_fallback,
};
use water::remote::SshTunnel;
use water::ui::{WaterApplication, ui_control_channel};

fn main() -> Result<()> {
    init_tracing();
    let arguments: Vec<String> = std::env::args().skip(1).collect();
    let explicit_server_mode = arguments
        .first()
        .is_some_and(|argument| argument == "server" || argument == "--server");
    if explicit_server_mode {
        run_server_via_dedicated_binary(&arguments[1..])
    } else {
        run_gui(arguments.into_iter())
    }
}

/// Preserve the compatibility `water server` spelling while replacing the
/// process image with `water-server` whenever the sibling binary is present.
/// This keeps Activity Monitor/ps labels unambiguous for manual starts too.
fn run_server_via_dedicated_binary(arguments: &[String]) -> Result<()> {
    let executable = std::env::current_exe().context("could not resolve water executable")?;
    let dedicated = executable.with_file_name(if cfg!(windows) {
        "water-server.exe"
    } else {
        "water-server"
    });
    if !dedicated.is_file() {
        return water::server::run(arguments.iter().cloned());
    }

    let mut command = std::process::Command::new(dedicated);
    command.args(arguments);
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt as _;
        Err(command.exec()).context("could not replace process with water-server")
    }
    #[cfg(not(unix))]
    {
        let status = command.status().context("could not start water-server")?;
        if status.success() {
            Ok(())
        } else {
            bail!("water-server exited with {status}")
        }
    }
}

/// GUI client: resolves (or auto-starts) the server, attaches, and renders.
fn run_gui(arguments: impl Iterator<Item = String>) -> Result<()> {
    let startup = parse_startup_options(arguments)?;
    let config = AppConfig::load_from_path(&startup.config_path)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let ssh_tunnel = startup
        .ssh_destination
        .as_deref()
        .map(SshTunnel::connect)
        .transpose()
        .context("could not connect to remote Water server")?;
    let socket_path = ssh_tunnel.as_ref().map_or_else(
        || resolve_socket_path(&startup, &config),
        |tunnel| Ok(tunnel.local_socket().to_path_buf()),
    )?;
    tracing::info!(
        target: "water::workspace",
        socket = %socket_path.display(),
        config = %startup.config_path.display(),
        initial_workspace = startup.initial_workspace.unwrap_or(config.startup.initial_workspace),
        initial_terminal = startup
            .initial_terminal
            .unwrap_or(config.startup.initial_terminal),
        "starting water client"
    );

    // Resolve a server: attach when one is already listening, auto-start
    // (detached or embedded) when none is.
    let probe = ControlClient::new(socket_path.clone());
    let initial_workspace = startup
        .initial_workspace
        .unwrap_or(config.startup.initial_workspace);
    let initial_terminal = startup
        .initial_terminal
        .unwrap_or(config.startup.initial_terminal)
        && initial_workspace;
    let mut embedded: Option<EmbeddedServer> = None;
    let mut owns_server = false;
    if probe.ping().is_err() {
        if !config.server.auto_start {
            bail!(
                "no water server is listening at {}; start one with `water server` \
                 or set server.auto_start",
                socket_path.display()
            );
        }
        if config.server.detached {
            spawn_detached_server(
                &socket_path,
                &startup.config_path,
                initial_workspace,
                initial_terminal,
            )?;
            wait_for_server(&socket_path)?;
            owns_server = true;
        } else {
            let mut host = ModelHost::start_with_config(config.clone());
            let client = host.client();
            ensure_initial_workspace(&client, initial_workspace, initial_terminal);
            let (handle, _shutdown_rx) = ControlServer::start(
                socket_path.clone(),
                client,
                None,
                Some(host.take_snapshot_receiver()),
            )
            .with_context(|| {
                format!(
                    "failed to start embedded server at {}",
                    socket_path.display()
                )
            })?;
            embedded = Some(EmbeddedServer {
                host,
                handle: Some(handle),
            });
        }
    }

    let transport: std::sync::Arc<dyn CommandTransport> =
        std::sync::Arc::new(RemoteCommandClient::connect(&socket_path)?);
    // Create the initial workspace/terminal when attaching to a fresh server
    // (for example one started manually with --empty-workspace); a server
    // that already has state is attached as-is, tmux-style.
    let initial = transport.state_dump()?;
    if initial.workspaces.is_empty() && (initial_workspace || initial_terminal) {
        ensure_initial_workspace(transport.as_ref(), initial_workspace, initial_terminal);
    }

    // Snapshot stream + UI automation: prefer the push session; fall back to
    // state polling against pre-split servers.
    let (ui_control_client, ui_control_receiver) = ui_control_channel();
    let snapshot_receiver = match connect_water_session(&socket_path, ui_control_client) {
        Ok(receiver) => receiver,
        Err(error) => {
            tracing::warn!(
                target: "water::workspace",
                ?error,
                "session.open unavailable; falling back to state polling"
            );
            spawn_state_polling_fallback(transport.clone())
        }
    };

    let detach_on_quit = config.server.detach_on_quit;
    let ui_application = WaterApplication::new_with_config_path(
        transport,
        initial,
        config,
        startup.config_path.clone(),
    );
    let shutdown_socket_path = socket_path.clone();
    ui_application.set_server_shutdown_handler(move || {
        if let Err(error) = ControlClient::new(shutdown_socket_path.clone()).server_shutdown() {
            tracing::warn!(
                target: "water::workspace",
                ?error,
                "failed to stop server while quitting GUI"
            );
        }
    });
    let reopen_application = ui_application.clone();
    let application =
        platform_application().with_restart_arguments(std::env::args_os().skip(1).collect());
    application.on_reopen(move |cx| reopen_application.reopen(cx));
    application.run(move |cx: &mut App| {
        ui_application.install(cx, snapshot_receiver, ui_control_receiver);
    });

    // The GUI is gone: detach the server (tmux semantics) or stop it when
    // this process owns it and detach_on_quit is off.
    if owns_server && !detach_on_quit {
        let _ = ControlClient::new(socket_path.clone()).server_shutdown();
    }
    if let Some(mut embedded) = embedded {
        if let Some(mut handle) = embedded.handle.take() {
            handle.shutdown();
        }
        embedded.host.shutdown();
    }
    Ok(())
}

/// The embedded server must outlive the GPUI run loop; this wrapper makes the
/// drop order explicit.
struct EmbeddedServer {
    host: ModelHost,
    handle: Option<water::control::ControlServerHandle>,
}

fn spawn_detached_server(
    socket_path: &PathBuf,
    config_path: &PathBuf,
    initial_workspace: bool,
    initial_terminal: bool,
) -> Result<std::process::Child> {
    let exe = std::env::current_exe().context("could not resolve water executable")?;
    let dedicated_server = exe.with_file_name(if cfg!(windows) {
        "water-server.exe"
    } else {
        "water-server"
    });
    let use_dedicated_server = dedicated_server.is_file();
    let mut command = std::process::Command::new(if use_dedicated_server {
        dedicated_server
    } else {
        exe
    });
    if !use_dedicated_server {
        command.arg("server");
    }
    command
        .arg("--control-socket")
        .arg(socket_path)
        .arg("--config")
        .arg(config_path);
    if !initial_workspace {
        command.arg("--empty-workspace");
    } else if !initial_terminal {
        command.arg("--no-initial-terminal");
    }
    command.stdin(std::process::Stdio::null());
    command.stdout(std::process::Stdio::null());
    // Detached server logs land next to the socket; the socket path is the
    // per-instance identity, so these never collide between instances.
    let log_path = format!("{}.server.log", socket_path.display());
    match std::fs::OpenOptions::new()
        .create(true)
        .append(true)
        .open(log_path)
    {
        Ok(file) => {
            command.stderr(std::process::Stdio::from(file));
        }
        Err(_) => {
            command.stderr(std::process::Stdio::null());
        }
    }
    #[cfg(unix)]
    {
        use std::os::unix::process::CommandExt as _;
        // SAFETY: the closure only calls setsid, which is async-signal-safe
        // and uses no Rust state; a race that leaves us a session leader is
        // harmless because the nulled stdio already detaches the process.
        unsafe {
            command.pre_exec(|| {
                libc::setsid();
                Ok(())
            });
        }
    }
    command.spawn().context("failed to start water server")
}

fn wait_for_server(socket_path: &Path) -> Result<()> {
    let client = ControlClient::new(socket_path);
    let deadline = std::time::Instant::now() + Duration::from_secs(15);
    while std::time::Instant::now() < deadline {
        if client.ping().is_ok() {
            return Ok(());
        }
        std::thread::sleep(Duration::from_millis(100));
    }
    bail!(
        "water server did not come up at {} within 15 seconds",
        socket_path.display()
    )
}

/// Creates the initial workspace (and terminal tab) on a fresh model. The
/// server does this at startup when it owns state creation; the GUI repeats
/// it only when it attaches to a server with no workspaces at all.
fn ensure_initial_workspace(
    client: &dyn CommandTransport,
    initial_workspace: bool,
    initial_terminal: bool,
) {
    if !initial_workspace {
        return;
    }
    if let Ok(state) = client.state_dump()
        && !state.workspaces.is_empty()
    {
        return;
    }
    dispatch_checked(client, AppCommand::Workspace(WorkspaceCommand::Create));
    if initial_terminal {
        dispatch_checked(client, AppCommand::Tab(TabCommand::New { title: None }));
    }
}

fn dispatch_checked(client: &dyn CommandTransport, command: AppCommand) {
    let operation_id = match client.dispatch(command) {
        Ok(operation_id) => operation_id,
        Err(error) => {
            tracing::error!(target: "water::workspace", ?error, "initial command failed to dispatch");
            return;
        }
    };
    let operation = match client.wait_operation(operation_id) {
        Ok(operation) => operation,
        Err(error) => {
            tracing::error!(target: "water::workspace", ?error, "initial command timed out");
            return;
        }
    };
    if operation.status == OperationStatus::Failed {
        let error = operation
            .error
            .map(|error| format!("{}: {}", error.code, error.message))
            .unwrap_or_else(|| "unknown command failure".to_owned());
        tracing::error!(target: "water::workspace", error, "initial command failed");
    }
}

struct StartupOptions {
    socket_path: Option<PathBuf>,
    ssh_destination: Option<String>,
    config_path: PathBuf,
    initial_workspace: Option<bool>,
    initial_terminal: Option<bool>,
}

fn parse_startup_options(mut args: impl Iterator<Item = String>) -> Result<StartupOptions> {
    let mut socket_path = None;
    let mut ssh_destination = None;
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
        } else if argument == "--ssh" {
            ssh_destination = Some(args.next().context("--ssh requires a destination")?);
        } else if let Some(destination) = argument.strip_prefix("--ssh=") {
            ssh_destination = Some(destination.to_owned());
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
                "water [--ssh HOST] [--control-socket PATH] [--config PATH] [--no-initial-terminal] [--empty-workspace] [server ...]"
            );
            std::process::exit(0);
        } else {
            bail!("unknown argument: {argument}");
        }
    }
    Ok(StartupOptions {
        socket_path,
        ssh_destination,
        config_path,
        initial_workspace,
        initial_terminal,
    })
}

/// Resolves the control/server socket: CLI flag > WATER_CONTROL_SOCKET >
/// `server.socket_path` > `startup.control_socket` > platform default.
fn resolve_socket_path(startup: &StartupOptions, config: &AppConfig) -> Result<PathBuf> {
    if let Some(path) = &startup.socket_path {
        return Ok(path.clone());
    }
    if let Some(path) = std::env::var_os("WATER_CONTROL_SOCKET") {
        return Ok(PathBuf::from(path));
    }
    if let Some(path) = &config.server.socket_path {
        return Ok(PathBuf::from(path));
    }
    if let Some(path) = &config.startup.control_socket {
        return Ok(PathBuf::from(path));
    }
    Ok(default_socket_path())
}

fn init_tracing() {
    let filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("water=info"));
    // Explicitly stderr: the default writer is stdout, which a detached
    // server points at /dev/null (its .server.log captures stderr).
    let _ = tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_writer(std::io::stderr)
        .try_init();
}
