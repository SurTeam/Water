use std::path::PathBuf;

use water::app::ModelHost;
use water::automation::{InProcessBackend, Scenario, ScenarioRunner};
use water::command::CommandDispatcher;
use water::control::{ControlClient, ControlServer};

#[test]
fn workspace_basic_scenario_runs_without_sleeps() {
    let scenario_path =
        PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("tests/scenarios/workspace_basic.json");
    let scenario = Scenario::from_path(scenario_path).expect("scenario parses");
    let mut dispatcher = CommandDispatcher::new();
    let mut runner = ScenarioRunner::new(InProcessBackend::new(&mut dispatcher));
    runner.run(&scenario).expect("workspace scenario passes");

    let state = dispatcher.state_dump();
    assert_eq!(state.workspace.as_ref().unwrap().tabs.len(), 1);
    assert_eq!(
        state.workspace.as_ref().unwrap().tabs[0].tree.pane_count(),
        1
    );
}

#[cfg(unix)]
#[test]
fn control_socket_uses_the_same_command_dispatch_path() {
    let socket_path =
        std::env::temp_dir().join(format!("water-phase1-{}.sock", std::process::id()));
    let mut host = ModelHost::start();
    let server_client = host.client();
    let mut server =
        ControlServer::start(socket_path.clone(), server_client).expect("server starts");
    let client = ControlClient::new(&socket_path);
    client.ping().expect("ping succeeds");

    let operation_id = client
        .dispatch(
            serde_json::from_value(serde_json::json!({
                "type": "workspace.create"
            }))
            .expect("command decodes"),
        )
        .expect("dispatch succeeds");
    let operation = client
        .wait_operation(operation_id)
        .expect("operation completes");
    assert!(operation.error.is_none());
    let state = client.state_dump().expect("state dump succeeds");
    assert!(state.workspace.is_some());

    server.shutdown();
    host.shutdown();
}
