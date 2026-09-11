//! Exercise the real packaging scripts with a fake compiler in an isolated tree.
//! This checks profile propagation and artifact selection without cross-compiling.
#![cfg(unix)]

use std::{
    fs,
    os::unix::fs::{PermissionsExt, symlink},
    path::PathBuf,
    process::Command,
};

struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

#[test]
fn dev_and_release_packages_keep_matching_servers_and_separate_outputs() {
    let nonce = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let root =
        Fixture(std::env::temp_dir().join(format!("water-package-{}-{nonce}", std::process::id())));
    for directory in ["scripts", "assets/macos", "bin", "dist"] {
        fs::create_dir_all(root.0.join(directory)).unwrap();
    }
    for (name, content) in [
        (
            "scripts/build-macos-app.sh",
            include_str!("../scripts/build-macos-app.sh"),
        ),
        (
            "scripts/build-embedded-servers.sh",
            include_str!("../scripts/build-embedded-servers.sh"),
        ),
        (
            "scripts/build-linux.sh",
            include_str!("../scripts/build-linux.sh"),
        ),
        ("Cargo.toml", "version = \"0.0.1\"\n"),
        ("assets/macos/Water.icns", "icon"),
        (
            "assets/macos/Info.plist.template",
            "__BUNDLE_ID__ __APP_NAME__ __WATER_VERSION__",
        ),
        ("bin/mock", MOCK),
    ] {
        fs::write(root.0.join(name), content).unwrap();
    }
    fs::set_permissions(root.0.join("bin/mock"), fs::Permissions::from_mode(0o755)).unwrap();
    for name in ["cargo", "rustup", "uname", "codesign", "plutil", "zig"] {
        symlink("mock", root.0.join("bin").join(name)).unwrap();
    }
    let path = format!(
        "{}:{}",
        root.0.join("bin").display(),
        std::env::var("PATH").unwrap()
    );
    for host in ["Darwin", "Linux"] {
        for variant in ["release", "dev"] {
            let output = Command::new("bash")
                .arg(root.0.join("scripts/build-macos-app.sh"))
                .env("PATH", &path)
                .env("MOCK_HOST", host)
                .env("WATER_APP_VARIANT", variant)
                .env_remove("WATER_SERVER_BUNDLE_DIR")
                .output()
                .unwrap();
            assert!(
                output.status.success(),
                "{}",
                String::from_utf8_lossy(&output.stderr)
            );
            let app = if variant == "dev" {
                "Water Dev"
            } else {
                "Water"
            };
            for binary in ["water", "water-server"] {
                let actual = fs::read_to_string(
                    root.0
                        .join(format!("dist/{app}.app/Contents/MacOS/{binary}")),
                )
                .unwrap();
                assert_eq!(actual, format!("{variant}\n"));
            }
            let bundle = root.0.join(format!("target/embedded-servers/{variant}"));
            assert_eq!(
                fs::read_to_string(bundle.join("variant")).unwrap().trim(),
                variant
            );
            for target in [
                "aarch64-apple-darwin",
                "x86_64-apple-darwin",
                "aarch64-unknown-linux-musl",
                "x86_64-unknown-linux-musl",
            ] {
                let output = Command::new("gzip")
                    .args(["-dc"])
                    .arg(bundle.join(format!("water-server-{target}.gz")))
                    .output()
                    .unwrap();
                assert!(output.status.success());
                assert_eq!(
                    String::from_utf8(output.stdout).unwrap(),
                    format!("{variant}\n")
                );
            }
        }
        // Building dev must preserve the existing release package.
        assert_eq!(
            fs::read_to_string(root.0.join("dist/Water.app/Contents/MacOS/water-server")).unwrap(),
            "release\n"
        );
    }
    for variant in ["release", "dev"] {
        let output = Command::new("bash")
            .arg(root.0.join("scripts/build-linux.sh"))
            .env("PATH", &path)
            .env("MOCK_HOST", "Linux")
            .env("WATER_APP_VARIANT", variant)
            .env_remove("WATER_SERVER_BUNDLE_DIR")
            .output()
            .unwrap();
        assert!(
            output.status.success(),
            "{}",
            String::from_utf8_lossy(&output.stderr)
        );
    }
    assert!(
        root.0
            .join("dist/water-0.0.1-aarch64-linux.tar.gz")
            .exists()
    );
    assert!(
        root.0
            .join("dist/water-dev-0.0.1-aarch64-linux.tar.gz")
            .exists()
    );
    assert!(root.0.join("dist/Water.app").exists());
}

const MOCK: &str = r#"#!/usr/bin/env bash
set -euo pipefail
case "${0##*/}" in
  uname) if [[ "${1:-}" == "-s" ]]; then echo "$MOCK_HOST"; else echo arm64; fi ;;
  codesign|plutil|zig) exit 0 ;;
  rustup)
    case "$1" in
      which) echo /usr/bin/true ;;
      target) printf '%s\n' aarch64-apple-darwin x86_64-apple-darwin aarch64-unknown-linux-musl x86_64-unknown-linux-musl ;;
      run) shift 2; exec "$@" ;;
      *) exit 1 ;;
    esac ;;
  cargo)
    profile=debug; variant=dev; target=""
    while (( $# )); do
      case "$1" in
        --release) profile=release; variant=release ;;
        --profile) shift; [[ "$1" == dev ]] || exit 9 ;;
        --target) shift; target="$1/" ;;
      esac
      shift
    done
    directory="target/$target$profile"
    mkdir -p "$directory"
    for binary in water water-server; do
      printf '%s\n' "$variant" > "$directory/$binary"
    done ;;
esac
"#;
