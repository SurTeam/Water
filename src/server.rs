//! Headless Water server entry point.
//!
//! This module deliberately has no GPUI dependency so `water-server` can be
//! cross-compiled as a small static musl executable for remote deployment.

use std::path::PathBuf;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::RecvTimeoutError;
use std::time::Duration;

use anyhow::{Context as _, Result, bail};

use crate::app::{CommandTransport, ModelHost};
use crate::command::{AppCommand, OperationStatus, TabCommand, WorkspaceCommand};
use crate::config::AppConfig;
use crate::control::{ControlServer, default_socket_path};

pub fn init_tracing() {
    let filter = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| tracing_subscriber::EnvFilter::new("water=info"));
    let _ = tracing_subscriber::fmt()
        .with_env_filter(filter)
        .with_writer(std::io::stderr)
        .try_init();
}

pub fn run(arguments: impl Iterator<Item = String>) -> Result<()> {
    let startup = parse_server_options(arguments)?;
    if startup.daemonize {
        daemonize().context("could not detach water-server")?;
    }
    init_tracing();
    set_server_process_name();
    let config = AppConfig::load_from_path(&startup.config_path)
        .map_err(|error| anyhow::anyhow!(error.to_string()))?;
    let socket_path = resolve_socket_path(&startup, &config);
    let mut model_host = ModelHost::start_with_config(config.clone());
    let client = model_host.client();
    let initial_workspace = startup
        .initial_workspace
        .unwrap_or(config.startup.initial_workspace);
    let initial_terminal = startup
        .initial_terminal
        .unwrap_or(config.startup.initial_terminal)
        && initial_workspace;
    ensure_initial_workspace(&client, initial_workspace, initial_terminal);
    let (mut control_server, shutdown_rx) = ControlServer::start(
        socket_path.clone(),
        client,
        None,
        Some(model_host.take_snapshot_receiver()),
    )
    .with_context(|| {
        format!(
            "failed to start control socket at {}",
            socket_path.display()
        )
    })?;
    tracing::info!(
        target: "water::workspace",
        pid = std::process::id(),
        socket = %socket_path.display(),
        "water server listening"
    );
    wait_for_shutdown(shutdown_rx);
    control_server.shutdown();
    model_host.shutdown();
    tracing::info!(target: "water::workspace", "water server stopped");
    Ok(())
}

struct ServerOptions {
    socket_path: Option<PathBuf>,
    config_path: PathBuf,
    initial_workspace: Option<bool>,
    initial_terminal: Option<bool>,
    daemonize: bool,
}

fn parse_server_options(mut args: impl Iterator<Item = String>) -> Result<ServerOptions> {
    let mut socket_path = None;
    let mut config_path = std::env::var_os("WATER_CONFIG")
        .map(PathBuf::from)
        .unwrap_or_else(AppConfig::default_load_path);
    let mut initial_workspace = None;
    let mut initial_terminal = None;
    let mut daemonize = false;
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
        } else if argument == "--daemonize" {
            daemonize = true;
        } else if argument == "--build-variant" {
            println!("{}", crate::BUILD_VARIANT);
            std::process::exit(0);
        } else if argument == "--version" || argument == "-V" {
            println!("water-server {}", env!("CARGO_PKG_VERSION"));
            std::process::exit(0);
        } else if argument == "--help" || argument == "-h" {
            println!(
                "water-server [--control-socket PATH] [--config PATH] [--no-initial-terminal] [--empty-workspace] [--daemonize]"
            );
            std::process::exit(0);
        } else {
            bail!("unknown server argument: {argument}");
        }
    }
    Ok(ServerOptions {
        socket_path,
        config_path,
        initial_workspace,
        initial_terminal,
        daemonize,
    })
}

#[cfg(unix)]
fn daemonize() -> std::io::Result<()> {
    // SAFETY: this runs before the model, PTY, control, or tracing threads are
    // created. The parent exits immediately so SSH can return; the child keeps
    // the caller's cwd and redirected stdio, then enters a session with no
    // controlling terminal.
    let pid = unsafe { libc::fork() };
    if pid < 0 {
        return Err(std::io::Error::last_os_error());
    }
    if pid > 0 {
        unsafe { libc::_exit(0) };
    }
    if unsafe { libc::setsid() } < 0 {
        return Err(std::io::Error::last_os_error());
    }
    Ok(())
}

#[cfg(not(unix))]
fn daemonize() -> std::io::Result<()> {
    Err(std::io::Error::new(
        std::io::ErrorKind::Unsupported,
        "daemon mode is only supported on Unix",
    ))
}

fn resolve_socket_path(startup: &ServerOptions, config: &AppConfig) -> PathBuf {
    if let Some(path) = &startup.socket_path {
        return path.clone();
    }
    if let Some(path) = std::env::var_os("WATER_CONTROL_SOCKET") {
        return PathBuf::from(path);
    }
    if let Some(path) = &config.server.socket_path {
        return PathBuf::from(path);
    }
    if let Some(path) = &config.startup.control_socket {
        return PathBuf::from(path);
    }
    default_socket_path()
}

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

fn set_server_process_name() {
    let name: &str = if env!("WATER_BUILD_PROFILE") != "release" {
        "water-srv-dev"
    } else {
        "water-server"
    };
    #[cfg(target_os = "macos")]
    {
        // setprogname lives in libdispatch (a private symbol, always present
        // on macOS because every process links libSystem). The water-server
        // binary does not reference it at link time, so resolve it at runtime
        // via dlsym(RTLD_DEFAULT, ...).
        unsafe extern "C" {
            fn dlopen(file: *const libc::c_char, flags: libc::c_int) -> *mut libc::c_void;
            fn dlsym(handle: *mut libc::c_void, symbol: *const libc::c_char) -> *mut libc::c_void;
            fn pthread_setname_np(name: *const libc::c_char) -> libc::c_int;
        }
        type SetPrognameFn = unsafe extern "C" fn(*const libc::c_char);
        const RTLD_DEFAULT: libc::c_int = -2;
        let c_name = std::ffi::CString::new(name).unwrap();
        let c_sym = std::ffi::CString::new("setprogname").unwrap();
        unsafe {
            let handle = dlopen(std::ptr::null(), RTLD_DEFAULT);
            let sym = dlsym(handle, c_sym.as_ptr());
            if !sym.is_null() {
                let set_progname: SetPrognameFn =
                    std::mem::transmute::<*mut libc::c_void, SetPrognameFn>(sym);
                set_progname(c_name.as_ptr());
            }
            let _ = pthread_setname_np(c_name.as_ptr());
        }
    }

    #[cfg(target_os = "linux")]
    unsafe {
        let c_name = std::ffi::CString::new(name).unwrap();
        let _ = libc::prctl(libc::PR_SET_NAME, c_name.as_ptr() as libc::c_ulong, 0, 0, 0);
    }
}

#[cfg(unix)]
fn wait_for_shutdown(shutdown_rx: std::sync::mpsc::Receiver<()>) {
    static SIGNAL_RECEIVED: AtomicBool = AtomicBool::new(false);
    unsafe extern "C" fn on_signal(_: libc::c_int) {
        SIGNAL_RECEIVED.store(true, Ordering::Release);
    }
    unsafe {
        let handler = on_signal as *const () as usize;
        libc::signal(libc::SIGINT, handler);
        libc::signal(libc::SIGTERM, handler);
        // A detached server must survive its launching SSH/control terminal.
        libc::signal(libc::SIGHUP, libc::SIG_IGN);
    }
    loop {
        match shutdown_rx.recv_timeout(Duration::from_millis(100)) {
            Ok(()) => break,
            Err(RecvTimeoutError::Timeout) => {
                if SIGNAL_RECEIVED.load(Ordering::Acquire) {
                    break;
                }
            }
            Err(RecvTimeoutError::Disconnected) => break,
        }
    }
}

#[cfg(not(unix))]
fn wait_for_shutdown(shutdown_rx: std::sync::mpsc::Receiver<()>) {
    let _ = shutdown_rx.recv();
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::path::Path;

    #[test]
    fn parses_portable_server_options_without_gui_types() {
        let options = parse_server_options(
            [
                "--control-socket=/tmp/water-test.sock",
                "--config=/tmp/water-test.json",
                "--empty-workspace",
                "--daemonize",
            ]
            .into_iter()
            .map(str::to_owned),
        )
        .unwrap();
        assert_eq!(
            options.socket_path.as_deref(),
            Some(Path::new("/tmp/water-test.sock"))
        );
        assert_eq!(options.config_path, Path::new("/tmp/water-test.json"));
        assert_eq!(options.initial_workspace, Some(false));
        assert_eq!(options.initial_terminal, Some(false));
        assert!(options.daemonize);
    }
}
