#![cfg(unix)]

use std::os::unix::fs::PermissionsExt;
use std::time::{Duration, Instant};
use water::{AppConfig, ModelHost};

#[test]
fn finder_file_url_executes_literal_path_in_its_directory() {
    let directory = std::env::temp_dir().join(format!("water-command-{}", uuid::Uuid::new_v4()));
    std::fs::create_dir(&directory).unwrap();
    let path = directory.join("spaces ' $(touch INJECTED) 中文.command");
    std::fs::write(&path, "#!/bin/sh\nprintf done > result\n").unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o700)).unwrap();
    let mut config = AppConfig::default();
    config.shell.program = "/bin/sh".into();
    config.shell.args = vec!["-i".into()];
    let mut host = ModelHost::start_with_config(config.clone());
    let client = host.client();
    let url = url::Url::from_file_path(&path).unwrap();
    water::command_file::open_command_file(&client, &config, url.as_str()).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    while std::fs::read_to_string(directory.join("result"))
        .ok()
        .as_deref()
        != Some("done")
        && Instant::now() < deadline
    {
        std::thread::yield_now();
    }
    assert_eq!(
        std::fs::read_to_string(directory.join("result")).unwrap(),
        "done"
    );
    assert!(!directory.join("INJECTED").exists());
    let state = client.state_dump().unwrap();
    assert_eq!(state.workspaces.len(), 1);
    assert_eq!(state.workspaces[0].tabs.len(), 1);
    host.shutdown();
    std::fs::remove_dir_all(directory).unwrap();
}

#[test]
fn rejects_nonlocal_missing_and_noncommand_files_without_creating_workspaces() {
    let mut host = ModelHost::start();
    let client = host.client();
    for url in [
        "https://example.com/test.command",
        "file://remote/tmp/test.command",
        "file:///missing.command",
        "file:///etc/hosts",
    ] {
        assert!(
            water::command_file::open_command_file(&client, &AppConfig::default(), url).is_err()
        );
    }
    assert!(client.state_dump().unwrap().workspaces.is_empty());
    host.shutdown();
}
