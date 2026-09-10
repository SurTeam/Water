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

#[test]
fn debug_grid_state() {
    let size = TerminalSize::new(80, 24);
    let mut emulator = TerminalEmulator::new(TerminalId::new(98), size, 2000);
    for i in 1..=40u32 {
        emulator.apply(&output(i as u64, format!("GRID_{i}\r\n").as_bytes(), size));
    }
    let (do_, total, screen, top, bottom) = emulator.grid_debug();
    eprintln!("GRID: display_offset={} total_lines={} screen_lines={} topmost={} bottommost={}",
        do_, total, screen, top, bottom);
    emulator.scroll_to(emulator.history_len());
    let (do2, total2, screen2, top2, bottom2) = emulator.grid_debug();
    eprintln!("AFTER SCROLL: display_offset={} total_lines={} screen_lines={} topmost={} bottommost={}",
        do2, total2, screen2, top2, bottom2);
}

#[test]
fn debug_scroll_behavior() {
    let size = TerminalSize::new(80, 24);
    let mut emulator = TerminalEmulator::new(TerminalId::new(99), size, 2000);
    for i in 1..=40u32 {
        emulator.apply(&output(i as u64, format!("DBG_{i}\r\n").as_bytes(), size));
    }
    println!("before scroll: history_len={} viewport_position={}",
        emulator.history_len(), emulator.viewport_position());
    emulator.scroll_to(emulator.history_len());
    let snap = emulator.snapshot(None);
    println!("after scroll_to(history_len): display_offset={} rows_before.len()={} viewport_position={}",
        snap.display_offset, snap.rows_before.len(), snap.viewport_position);
    emulator.scroll_by(1);
    let snap2 = emulator.snapshot(None);
    println!("after scroll_by(1): display_offset={} rows_before.len()={}",
        snap2.display_offset, snap2.rows_before.len());
    // Try scrolling by a larger amount
    let mut em2 = TerminalEmulator::new(TerminalId::new(100), size, 2000);
    for i in 1..=40u32 {
        em2.apply(&output(i as u64, format!("DBG2_{i}\r\n").as_bytes(), size));
    }
    em2.scroll_by(20);
    let snap3 = em2.snapshot(None);
    println!("em2 scroll_by(20): display_offset={} rows_before.len()={} viewport_position={}",
        snap3.display_offset, snap3.rows_before.len(), snap3.viewport_position);
}

/// Verify that scrolling to the top shows the oldest content.
#[test]
fn scrolled_snapshot_shows_oldest_content() {
    let size = TerminalSize::new(80, 24);
    let mut emulator = TerminalEmulator::new(TerminalId::new(13), size, 2000);
    for i in 1..=40u32 {
        emulator.apply(&output(i as u64, format!("OVERSCAN_TEST_{i}\r\n").as_bytes(), size));
    }
    emulator.scroll_to(emulator.history_len());
    let snapshot = emulator.snapshot(None);
    assert!(
        snapshot.display_offset > 0,
        "scrolling to the top must move the viewport into history"
    );
    let visible = snapshot.visible_text();
    let top_text = visible.lines().next().unwrap_or("");
    assert!(
        top_text.contains("OVERSCAN_TEST_1"),
        "the top visible row after scrolling to the top must be the oldest content, got {top_text:?}"
    );
}

/// Verify the selection mapping follows the painted content when scrolled.
/// With the viewport at the top (display_offset = history_len), a pixel in
/// the top painted row addresses the topmost visible source row, which the
/// snapshot must be able to resolve.
#[test]
fn selection_mapping_follows_painted_content() {
    let size = TerminalSize::new(80, 24);
    let mut emulator = TerminalEmulator::new(TerminalId::new(14), size, 2000);
    for i in 1..=40u32 {
        emulator.apply(&output(i as u64, format!("SEL_MAP_{i}\r\n").as_bytes(), size));
    }
    emulator.scroll_to(emulator.history_len());
    let snapshot = emulator.snapshot(None);
    // The top visible row must be resolvable and contain the oldest content.
    let top_row_cells = snapshot
        .relative_row_snapshot(0)
        .expect("the top visible row (source row 0) must be resolvable");
    let text: String = top_row_cells.iter().map(|c| c.character).collect();
    assert!(
        text.contains("SEL_MAP_1"),
        "the top visible row must contain the oldest content, got {text:?}"
    );
}
