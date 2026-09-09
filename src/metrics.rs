//! Lightweight process-global counters for the terminal hot path.
//!
//! Deliberately small: fixed atomic counters, no per-event allocation, and a
//! 5 s reporter that only runs when `WATER_METRICS=1` is set. The
//! `debug.metrics` RPC exposes the same counters so benchmarks can sample
//! absolute values without parsing logs.

use std::sync::OnceLock;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::Duration;

pub fn pty_bytes_read() -> &'static AtomicU64 {
    &PTY_BYTES_READ
}

pub fn pty_read_calls() -> &'static AtomicU64 {
    &PTY_READ_CALLS
}

pub fn terminal_output_bytes_sent() -> &'static AtomicU64 {
    &TERMINAL_OUTPUT_BYTES_SENT
}

pub fn terminal_stream_events() -> &'static AtomicU64 {
    &TERMINAL_STREAM_EVENTS
}

pub fn terminal_resize_events() -> &'static AtomicU64 {
    &TERMINAL_RESIZE_EVENTS
}

pub fn state_dumps() -> &'static AtomicU64 {
    &STATE_DUMPS
}

pub fn model_snapshot_pushes() -> &'static AtomicU64 {
    &MODEL_SNAPSHOT_PUSSES
}

pub fn terminal_bytes_received() -> &'static AtomicU64 {
    &TERMINAL_BYTES_RECEIVED
}

pub fn processor_advances() -> &'static AtomicU64 {
    &PROCESSOR_ADVANCES
}

pub fn terminal_bytes_advanced() -> &'static AtomicU64 {
    &TERMINAL_BYTES_ADVANCED
}

pub fn terminal_notifies() -> &'static AtomicU64 {
    &TERMINAL_NOTIFIES
}

pub fn terminal_renders() -> &'static AtomicU64 {
    &TERMINAL_RENDERS
}

pub fn hidden_terminal_updates() -> &'static AtomicU64 {
    &HIDDEN_TERMINAL_UPDATES
}

pub fn replay_bytes_received() -> &'static AtomicU64 {
    &REPLAY_BYTES_RECEIVED
}

/// Gauge: total bytes currently held in all server replay rings.
pub fn add_replay_ring_bytes(delta: isize) {
    if delta >= 0 {
        REPLAY_RING_BYTES.fetch_add(delta as u64, Ordering::Relaxed);
    } else {
        REPLAY_RING_BYTES.fetch_sub(delta.unsigned_abs() as u64, Ordering::Relaxed);
    }
}

pub fn replay_ring_bytes() -> u64 {
    REPLAY_RING_BYTES.load(Ordering::Relaxed)
}

static PTY_BYTES_READ: AtomicU64 = AtomicU64::new(0);
static PTY_READ_CALLS: AtomicU64 = AtomicU64::new(0);
static TERMINAL_OUTPUT_BYTES_SENT: AtomicU64 = AtomicU64::new(0);
static TERMINAL_STREAM_EVENTS: AtomicU64 = AtomicU64::new(0);
static TERMINAL_RESIZE_EVENTS: AtomicU64 = AtomicU64::new(0);
static STATE_DUMPS: AtomicU64 = AtomicU64::new(0);
static MODEL_SNAPSHOT_PUSSES: AtomicU64 = AtomicU64::new(0);
static TERMINAL_BYTES_RECEIVED: AtomicU64 = AtomicU64::new(0);
static PROCESSOR_ADVANCES: AtomicU64 = AtomicU64::new(0);
static TERMINAL_BYTES_ADVANCED: AtomicU64 = AtomicU64::new(0);
static TERMINAL_NOTIFIES: AtomicU64 = AtomicU64::new(0);
static TERMINAL_RENDERS: AtomicU64 = AtomicU64::new(0);
static HIDDEN_TERMINAL_UPDATES: AtomicU64 = AtomicU64::new(0);
static REPLAY_BYTES_RECEIVED: AtomicU64 = AtomicU64::new(0);
static REPLAY_RING_BYTES: AtomicU64 = AtomicU64::new(0);

pub fn inc(counter: &AtomicU64) {
    counter.fetch_add(1, Ordering::Relaxed);
}

pub fn add(counter: &AtomicU64, value: usize) {
    counter.fetch_add(value as u64, Ordering::Relaxed);
}

const RATE_COUNTERS: [&str; 14] = [
    "pty_bytes_read",
    "pty_read_calls",
    "terminal_output_bytes_sent",
    "terminal_stream_events",
    "terminal_resize_events",
    "state_dumps",
    "model_snapshot_pushes",
    "terminal_bytes_received",
    "processor_advances",
    "terminal_bytes_advanced",
    "terminal_notifies",
    "terminal_renders",
    "hidden_terminal_updates",
    "replay_bytes_received",
];

/// Absolute counter values as JSON, for the `debug.metrics` RPC.
pub fn snapshot() -> serde_json::Value {
    let mut value = serde_json::Map::new();
    let advances = processor_advances().load(Ordering::Relaxed);
    let advanced_bytes = terminal_bytes_advanced().load(Ordering::Relaxed);
    value.insert(
        "pty_bytes_read".to_owned(),
        serde_json::json!(pty_bytes_read().load(Ordering::Relaxed)),
    );
    value.insert(
        "pty_read_calls".to_owned(),
        serde_json::json!(pty_read_calls().load(Ordering::Relaxed)),
    );
    value.insert(
        "terminal_output_bytes_sent".to_owned(),
        serde_json::json!(terminal_output_bytes_sent().load(Ordering::Relaxed)),
    );
    value.insert(
        "terminal_stream_events".to_owned(),
        serde_json::json!(terminal_stream_events().load(Ordering::Relaxed)),
    );
    value.insert(
        "terminal_resize_events".to_owned(),
        serde_json::json!(terminal_resize_events().load(Ordering::Relaxed)),
    );
    value.insert(
        "state_dumps".to_owned(),
        serde_json::json!(state_dumps().load(Ordering::Relaxed)),
    );
    value.insert(
        "model_snapshot_pushes".to_owned(),
        serde_json::json!(model_snapshot_pushes().load(Ordering::Relaxed)),
    );
    value.insert(
        "replay_ring_bytes".to_owned(),
        serde_json::json!(replay_ring_bytes()),
    );
    value.insert(
        "terminal_bytes_received".to_owned(),
        serde_json::json!(terminal_bytes_received().load(Ordering::Relaxed)),
    );
    value.insert("processor_advances".to_owned(), serde_json::json!(advances));
    value.insert(
        "terminal_bytes_advanced".to_owned(),
        serde_json::json!(terminal_bytes_advanced().load(Ordering::Relaxed)),
    );
    value.insert(
        "bytes_per_advance".to_owned(),
        serde_json::json!(if advances == 0 {
            0.0
        } else {
            advanced_bytes as f64 / advances as f64
        }),
    );
    value.insert(
        "terminal_notifies".to_owned(),
        serde_json::json!(terminal_notifies().load(Ordering::Relaxed)),
    );
    value.insert(
        "terminal_renders".to_owned(),
        serde_json::json!(terminal_renders().load(Ordering::Relaxed)),
    );
    value.insert(
        "hidden_terminal_updates".to_owned(),
        serde_json::json!(hidden_terminal_updates().load(Ordering::Relaxed)),
    );
    value.insert(
        "replay_bytes_received".to_owned(),
        serde_json::json!(replay_bytes_received().load(Ordering::Relaxed)),
    );
    serde_json::Value::Object(value)
}

/// Starts the 5 s rate reporter. No-op unless `WATER_METRICS` is set.
pub fn start_reporter() {
    static STARTED: OnceLock<()> = OnceLock::new();
    STARTED.get_or_init(|| {
        if std::env::var_os("WATER_METRICS").is_none() {
            return;
        }
        let _ = std::thread::Builder::new()
            .name("water-metrics".to_owned())
            .spawn(|| {
                let mut last = snapshot();
                loop {
                    std::thread::sleep(Duration::from_secs(5));
                    let current = snapshot();
                    let mut line = String::new();
                    for key in RATE_COUNTERS {
                        let delta = current[key]
                            .as_u64()
                            .unwrap_or(0)
                            .saturating_sub(last[key].as_u64().unwrap_or(0));
                        if !line.is_empty() {
                            line.push(' ');
                        }
                        line.push_str(&format!("{key}/5s={delta}"));
                    }
                    line.push_str(&format!(
                        " replay_ring_bytes={}",
                        current["replay_ring_bytes"].as_u64().unwrap_or(0)
                    ));
                    tracing::info!(target: "water::metrics", stats = %line, "terminal metrics");
                    last = current;
                }
            });
    });
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn snapshot_reports_absolute_counters() {
        inc(pty_read_calls());
        add(pty_bytes_read(), 42);
        let value = snapshot();
        assert!(value["pty_read_calls"].as_u64().unwrap() >= 1);
        assert!(value["pty_bytes_read"].as_u64().unwrap() >= 42);
        assert!(value["replay_ring_bytes"].is_u64());
    }
}
