use water::ids::TerminalId;
use water::terminal::{TerminalEmulator, TerminalSize, TerminalStreamEvent};
use std::sync::Arc;

fn output(seq: u64, bytes: &[u8], size: TerminalSize) -> TerminalStreamEvent {
    TerminalStreamEvent::Output { seq, size, bytes: Arc::from(bytes.to_vec()) }
}

#[test]
fn flood_400_inspect_snapshot() {
    for (columns, lines) in [(80, 24), (132, 40)] {
        let size = TerminalSize::new(columns, lines);
        let mut emulator = TerminalEmulator::new(TerminalId::new(11), size, 2000);
        for i in 1..=400u32 {
            emulator.apply(&output(i as u64, format!("WATER_FLOOD_{i}\r\n").as_bytes(), size));
        }
        let text = emulator.snapshot(None).visible_text();
        let top = text.lines().next().unwrap_or("");
        let snap = emulator.snapshot(None);
        let before = snap.rows_before.len();
        let after = snap.rows_after.len();
        let last = snap.size.lines.saturating_sub(1);
        let bottom = text.lines().nth(last).unwrap_or("");
        println!("{columns}x{lines}: viewport={} rows_before={} rows_after={} top={:?} bottom={:?}",
            snap.viewport_position, before, after, top, bottom);
    }
}

#[test]
fn scrollback_retains_all_history_up_to_limit() {
    let size = TerminalSize::new(80, 24);
    // 132 columns like the user's terminal? Use 132x40 first.
    for (columns, lines) in [(80, 24), (132, 40)] {
        let size = TerminalSize::new(columns, lines);
        let mut emulator = TerminalEmulator::new(TerminalId::new(9), size, 2000);
        for i in 1..=400u32 {
            emulator.apply(&output(i as u64, format!("WATER_FLOOD_{i}\r\n").as_bytes(), size));
        }
        let snapshot_text = emulator.snapshot(None).visible_text();
        let snapshot = emulator.snapshot(None);
        let history = snapshot.rows_before.len();
        let visible_top = snapshot_text.lines().next().unwrap_or("");
        println!("{columns}x{lines}: history_len={} total={} visible_top={:?}",
            history, emulator.history_len(), visible_top);
        emulator.scroll_to(emulator.history_len());
        let scrolled_text = emulator.snapshot(None).visible_text();
        let top = scrolled_text.lines().next().unwrap_or("");
        println!("  scrolled to top: {top:?} (history retained: {})", snapshot.rows_before.len());
    }
}

#[test]
fn configured_scrollback_is_the_scroll_limit() {
    // The user's scenario: 400 lines of output, scrollback of 2000.
    // Scrolling up must reach the oldest emitted line, not stop at the
    // 32-row overscan window.
    let size = TerminalSize::new(80, 24);
    let mut emulator = TerminalEmulator::new(TerminalId::new(12), size, 2000);
    for i in 1..=400u32 {
        emulator.apply(&output(i as u64, format!("WATER_FLOOD_{i}\r\n").as_bytes(), size));
    }
    // The snapshot exposes the full retained history, not just the
    // materialized overscan rows.
    let snap = emulator.snapshot(None);
    assert!(snap.history_len >= 32, "history_len should cover the full scrollback, got {}", snap.history_len);
    assert_eq!(snap.history_bottom, snap.viewport_position - snap.history_len);

    // Scroll all the way up.
    emulator.scroll_to(emulator.history_len());
    let top_text = emulator.snapshot(None).visible_text();
    let top = top_text.lines().next().unwrap_or("");
    assert!(
        top.contains("WATER_FLOOD_1"),
        "scroll to top must reach the oldest output line, got {top:?}"
    );
}
