use std::path::{Path, PathBuf};

const TARGETS: [&str; 4] = [
    "aarch64-apple-darwin",
    "x86_64-apple-darwin",
    "aarch64-unknown-linux-musl",
    "x86_64-unknown-linux-musl",
];

fn main() {
    println!("cargo:rerun-if-env-changed=WATER_SERVER_BUNDLE_DIR");
    println!("cargo:rerun-if-env-changed=WATER_REQUIRE_EMBEDDED_SERVERS");
    println!("cargo:rerun-if-env-changed=WATER_BUILDING_PORTABLE_SERVER");
    let output = PathBuf::from(std::env::var_os("OUT_DIR").expect("OUT_DIR is set by Cargo"));
    let building_portable_server = std::env::var_os("WATER_BUILDING_PORTABLE_SERVER").is_some();
    let bundle_dir = (!building_portable_server)
        .then(|| std::env::var_os("WATER_SERVER_BUNDLE_DIR").map(PathBuf::from))
        .flatten();
    let require_payloads =
        !building_portable_server && std::env::var_os("WATER_REQUIRE_EMBEDDED_SERVERS").is_some();

    for target in TARGETS {
        let file_name = format!("water-server-{target}.gz");
        let destination = output.join(&file_name);
        let source = bundle_dir
            .as_deref()
            .map(|directory| directory.join(&file_name));
        match source.as_deref().filter(|path| path.is_file()) {
            Some(source) => {
                println!("cargo:rerun-if-changed={}", source.display());
                std::fs::copy(source, &destination).unwrap_or_else(|error| {
                    panic!(
                        "could not copy embedded server {}: {error}",
                        source.display()
                    )
                });
            }
            None if require_payloads => {
                let expected = source.unwrap_or_else(|| Path::new(&file_name).to_path_buf());
                panic!(
                    "missing embedded server payload {}; run scripts/build-embedded-servers.sh",
                    expected.display()
                );
            }
            None => {
                // Ordinary check/test builds stay fast. Release packaging sets
                // WATER_REQUIRE_EMBEDDED_SERVERS so it can never silently ship
                // a GUI without its deployment payloads.
                std::fs::write(destination, []).expect("write empty server placeholder");
            }
        }
    }
}
