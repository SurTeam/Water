#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct EmbeddedServerPayload {
    pub target: &'static str,
    pub gzip: &'static [u8],
}

const PAYLOADS: [EmbeddedServerPayload; 4] = [
    EmbeddedServerPayload {
        target: "aarch64-apple-darwin",
        gzip: include_bytes!(concat!(
            env!("OUT_DIR"),
            "/water-server-aarch64-apple-darwin.gz"
        )),
    },
    EmbeddedServerPayload {
        target: "x86_64-apple-darwin",
        gzip: include_bytes!(concat!(
            env!("OUT_DIR"),
            "/water-server-x86_64-apple-darwin.gz"
        )),
    },
    EmbeddedServerPayload {
        target: "aarch64-unknown-linux-musl",
        gzip: include_bytes!(concat!(
            env!("OUT_DIR"),
            "/water-server-aarch64-unknown-linux-musl.gz"
        )),
    },
    EmbeddedServerPayload {
        target: "x86_64-unknown-linux-musl",
        gzip: include_bytes!(concat!(
            env!("OUT_DIR"),
            "/water-server-x86_64-unknown-linux-musl.gz"
        )),
    },
];

pub(crate) fn payload_for_target(target: &str) -> Option<EmbeddedServerPayload> {
    PAYLOADS
        .iter()
        .copied()
        .find(|payload| payload.target == target && !payload.gzip.is_empty())
}

pub(crate) fn target_for_uname(system: &str, machine: &str) -> Option<&'static str> {
    match (system.trim(), machine.trim()) {
        ("Darwin", "arm64" | "aarch64") => Some("aarch64-apple-darwin"),
        ("Darwin", "x86_64" | "amd64") => Some("x86_64-apple-darwin"),
        ("Linux", "aarch64" | "arm64") => Some("aarch64-unknown-linux-musl"),
        ("Linux", "x86_64" | "amd64") => Some("x86_64-unknown-linux-musl"),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn maps_supported_remote_uname_pairs() {
        assert_eq!(
            target_for_uname("Linux", "x86_64"),
            Some("x86_64-unknown-linux-musl")
        );
        assert_eq!(
            target_for_uname("Linux", "aarch64"),
            Some("aarch64-unknown-linux-musl")
        );
        assert_eq!(
            target_for_uname("Darwin", "arm64"),
            Some("aarch64-apple-darwin")
        );
        assert_eq!(
            target_for_uname("Darwin", "x86_64"),
            Some("x86_64-apple-darwin")
        );
        assert_eq!(target_for_uname("FreeBSD", "x86_64"), None);
    }

    #[test]
    fn ordinary_development_builds_do_not_claim_empty_payloads() {
        for target in [
            "aarch64-apple-darwin",
            "x86_64-apple-darwin",
            "aarch64-unknown-linux-musl",
            "x86_64-unknown-linux-musl",
        ] {
            if let Some(payload) = payload_for_target(target) {
                assert!(!payload.gzip.is_empty());
            }
        }
    }
}
