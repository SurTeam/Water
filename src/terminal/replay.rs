//! Bounded in-memory replay history of raw terminal events.
//!
//! The ring stores what a late-attaching client needs to rebuild terminal
//! state from scratch: the raw output bytes and the geometry ordering.
//! Every stored event carries the size it was produced at, so eviction of
//! the oldest events can never lose the geometry context a replier needs.
//!
//! Eviction policy (predictable, by bytes):
//! - the oldest whole event is dropped first, repeatedly, until the ring is
//!   within its byte budget;
//! - the most recent event is never dropped;
//! - if the ring holds a single event over budget, that event's bytes are
//!   trimmed from the front (the tail of the stream survives, as in a
//!   `history-limit`-style scrollback).
//!
//! `ReplayBegin` consumers read [`ReplayRing::first_seq`] /
//! [`ReplayRing::first_size`] to learn the replay start point.

use std::collections::VecDeque;
use std::sync::{Arc, Mutex};

use super::snapshot::TerminalSize;
use super::stream::{TerminalSeq, TerminalStreamEvent};
use crate::metrics;

/// Hard byte ceiling for one terminal's ring (clamped at construction).
/// Kept below the control protocol's 16 MiB frame ceiling after base64
/// expansion, since protocol v2 transfers replay in one
/// attach response before switching to live event frames.
pub const MAX_REPLAY_BYTES: usize = 8 * 1024 * 1024;
/// Conservative charge for the deque slot, sequence/geometry, enum, and
/// allocator bookkeeping of every retained event. Charging metadata as well
/// as output prevents a resize-only stream from growing without bound.
const EVENT_OVERHEAD_BYTES: usize = 64;

enum StoredKind {
    Output(Arc<[u8]>),
    Resize,
    Exit(Option<i32>),
}

impl StoredKind {
    fn output_bytes(&self) -> usize {
        match self {
            Self::Output(bytes) => bytes.len(),
            _ => 0,
        }
    }
}

struct StoredEvent {
    seq: TerminalSeq,
    size: TerminalSize,
    kind: StoredKind,
}

impl StoredEvent {
    fn retained_bytes(&self) -> usize {
        EVENT_OVERHEAD_BYTES + self.kind.output_bytes()
    }

    fn to_stream(&self) -> TerminalStreamEvent {
        match &self.kind {
            StoredKind::Output(bytes) => TerminalStreamEvent::Output {
                seq: self.seq,
                size: self.size,
                bytes: bytes.clone(),
            },
            StoredKind::Resize => TerminalStreamEvent::Resize {
                seq: self.seq,
                size: self.size,
            },
            StoredKind::Exit(code) => TerminalStreamEvent::Exit {
                seq: self.seq,
                code: *code,
            },
        }
    }
}

struct RingInner {
    events: VecDeque<StoredEvent>,
    next_seq: TerminalSeq,
    bytes: usize,
    budget: usize,
}

impl RingInner {
    fn new(size: TerminalSize, budget: usize) -> Self {
        // AppConfig applies the user-facing minimum. Keeping the primitive
        // usable with tiny budgets makes eviction deterministic and directly
        // testable while still enforcing the absolute ceiling here.
        let budget = budget.clamp(EVENT_OVERHEAD_BYTES, MAX_REPLAY_BYTES);
        // Sequence 1 is the initial geometry anchor, so a client replaying
        // from the start always learns the spawn size first.
        let mut inner = Self {
            events: VecDeque::new(),
            next_seq: 1,
            bytes: 0,
            budget,
        };
        let anchor = TerminalStreamEvent::Resize { seq: 1, size };
        inner.next_seq += 1;
        inner.push(anchor);
        inner
    }

    fn reserve_seq(&mut self) -> TerminalSeq {
        let seq = self.next_seq;
        self.next_seq += 1;
        seq
    }

    fn push(&mut self, event: TerminalStreamEvent) {
        match &event {
            TerminalStreamEvent::Output { bytes, .. } => {
                self.events.push_back(StoredEvent {
                    seq: event.seq(),
                    size: event.size().unwrap_or_default(),
                    kind: StoredKind::Output(bytes.clone()),
                });
                self.bytes += EVENT_OVERHEAD_BYTES + bytes.len();
            }
            TerminalStreamEvent::Resize { size, .. } => {
                self.events.push_back(StoredEvent {
                    seq: event.seq(),
                    size: *size,
                    kind: StoredKind::Resize,
                });
                self.bytes += EVENT_OVERHEAD_BYTES;
            }
            TerminalStreamEvent::Exit { .. } => {
                self.events.push_back(StoredEvent {
                    seq: event.seq(),
                    size: self.events.back().map(|e| e.size).unwrap_or_default(),
                    kind: StoredKind::Exit(None),
                });
                self.bytes += EVENT_OVERHEAD_BYTES;
            }
        }
        self.evict();
    }

    fn assign_exit(&mut self, code: Option<i32>) -> TerminalSeq {
        let seq = self.reserve_seq();
        let size = self.events.back().map(|e| e.size).unwrap_or_default();
        self.events.push_back(StoredEvent {
            seq,
            size,
            kind: StoredKind::Exit(code),
        });
        self.bytes += EVENT_OVERHEAD_BYTES;
        self.evict();
        seq
    }

    fn evict(&mut self) {
        while self.bytes > self.budget {
            match (self.events.get(0), self.events.len()) {
                (Some(_oldest), 1) => match &mut self.events[0].kind {
                    StoredKind::Output(bytes)
                        if EVENT_OVERHEAD_BYTES + bytes.len() > self.budget =>
                    {
                        let keep = self.budget.saturating_sub(EVENT_OVERHEAD_BYTES);
                        let drop = bytes.len().saturating_sub(keep);
                        let trimmed: Arc<[u8]> = bytes[drop..].to_vec().into();
                        self.bytes -= drop;
                        *bytes = trimmed;
                        return;
                    }
                    _ => return,
                },
                (Some(oldest), _) => {
                    self.bytes = self.bytes.saturating_sub(oldest.retained_bytes());
                    self.events.pop_front();
                }
                (None, _) => return,
            }
        }
    }

    /// Keeps only the newest events that fit in `bytes`; used when a
    /// terminal is retired so closed tabs cannot pin full history.
    fn trim_to(&mut self, bytes: usize) {
        self.budget = bytes.clamp(EVENT_OVERHEAD_BYTES, self.budget);
        self.evict();
    }
}

/// Shared, mutex-guarded replay history. The PTY worker appends; the model
/// thread and attach/capture paths take read snapshots.
pub struct ReplayRing {
    inner: Mutex<RingInner>,
}

impl std::fmt::Debug for ReplayRing {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.debug_struct("ReplayRing")
            .field("events", &self.event_count())
            .field("bytes", &self.total_bytes())
            .finish()
    }
}

impl ReplayRing {
    pub fn new(size: TerminalSize, budget: usize) -> Arc<Self> {
        let inner = RingInner::new(size, budget);
        metrics::add_replay_ring_bytes(inner.bytes as isize);
        Arc::new(Self {
            inner: Mutex::new(inner),
        })
    }

    pub fn push_output(&self, size: TerminalSize, bytes: Arc<[u8]>) -> TerminalSeq {
        let mut inner = self.inner.lock().expect("replay ring poisoned");
        let before = inner.bytes;
        let seq = inner.reserve_seq();
        inner.push(TerminalStreamEvent::Output { seq, size, bytes });
        metrics::add_replay_ring_bytes(inner.bytes as isize - before as isize);
        seq
    }

    pub fn push_resize(&self, size: TerminalSize) -> TerminalSeq {
        let mut inner = self.inner.lock().expect("replay ring poisoned");
        let before = inner.bytes;
        let seq = inner.reserve_seq();
        inner.push(TerminalStreamEvent::Resize { seq, size });
        metrics::add_replay_ring_bytes(inner.bytes as isize - before as isize);
        seq
    }

    /// Records the exit and returns the Exit event's sequence.
    pub fn finish(&self, code: Option<i32>) -> TerminalSeq {
        let mut inner = self.inner.lock().expect("replay ring poisoned");
        let before = inner.bytes;
        let seq = inner.assign_exit(code);
        metrics::add_replay_ring_bytes(inner.bytes as isize - before as isize);
        seq
    }

    pub fn is_finished(&self) -> bool {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .back()
            .is_some_and(|event| matches!(event.kind, StoredKind::Exit(_)))
    }

    pub fn exit_code(&self) -> Option<Option<i32>> {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .back()
            .and_then(|event| match &event.kind {
                StoredKind::Exit(code) => Some(*code),
                _ => None,
            })
    }

    /// Sequence of the newest stored event.
    pub fn last_seq(&self) -> TerminalSeq {
        let inner = self.inner.lock().expect("replay ring poisoned");
        inner.events.back().map(|e| e.seq).unwrap_or(0)
    }

    /// Sequence of the oldest surviving event (the replay start point).
    pub fn first_seq(&self) -> Option<TerminalSeq> {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .front()
            .map(|event| event.seq)
    }

    /// Geometry of the oldest surviving event (the replay start geometry).
    pub fn first_size(&self) -> Option<TerminalSize> {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .front()
            .map(|event| event.size)
    }

    /// Geometry of the newest event (the terminal's current size).
    pub fn last_size(&self) -> Option<TerminalSize> {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .back()
            .map(|event| event.size)
    }

    pub fn total_bytes(&self) -> usize {
        self.inner.lock().expect("replay ring poisoned").bytes
    }

    pub fn event_count(&self) -> usize {
        self.inner
            .lock()
            .expect("replay ring poisoned")
            .events
            .len()
    }

    /// O(n) byte search over the retained raw output events. Used by the
    /// `waterctl terminal contains` fast path; a single pass over owned
    /// bytes, cheap at ring budget scale.
    pub fn raw_contains(&self, needle: &str) -> bool {
        let needle = needle.as_bytes();
        if needle.is_empty() {
            return true;
        }
        let inner = self.inner.lock().expect("replay ring poisoned");
        inner.events.iter().any(|event| match &event.kind {
            StoredKind::Output(bytes) => bytes.windows(needle.len()).any(|window| window == needle),
            _ => false,
        })
    }

    /// The full ordered history as stream events (replay for a new client).
    pub fn to_stream(&self) -> Vec<TerminalStreamEvent> {
        let inner = self.inner.lock().expect("replay ring poisoned");
        inner.events.iter().map(|event| event.to_stream()).collect()
    }

    pub fn trim_to(&self, bytes: usize) {
        let mut inner = self.inner.lock().expect("replay ring poisoned");
        let before = inner.bytes;
        inner.trim_to(bytes);
        metrics::add_replay_ring_bytes(inner.bytes as isize - before as isize);
    }
}

impl Drop for ReplayRing {
    fn drop(&mut self) {
        if let Ok(inner) = self.inner.lock() {
            metrics::add_replay_ring_bytes(-(inner.bytes as isize));
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn bytes(data: &[u8]) -> Arc<[u8]> {
        Arc::from(data.to_vec())
    }

    #[test]
    fn replay_starts_at_the_initial_geometry_anchor() {
        let ring = ReplayRing::new(TerminalSize::new(80, 24), 64 * 1024);
        assert_eq!(ring.first_seq(), Some(1));
        assert_eq!(ring.first_size(), Some(TerminalSize::new(80, 24)));
        assert_eq!(ring.last_seq(), 1);
        let stream = ring.to_stream();
        assert_eq!(stream.len(), 1);
        assert!(matches!(
            stream[0],
            TerminalStreamEvent::Resize {
                seq: 1,
                size: TerminalSize {
                    columns: 80,
                    lines: 24
                }
            }
        ));
    }

    #[test]
    fn output_and_resize_stay_strictly_ordered() {
        let ring = ReplayRing::new(TerminalSize::new(80, 24), 64 * 1024);
        let size = TerminalSize::new(80, 24);
        assert_eq!(ring.push_output(size, bytes(b"hello ")), 2);
        assert_eq!(ring.push_output(size, bytes(b"world")), 3);
        let wide = TerminalSize::new(120, 40);
        assert_eq!(ring.push_resize(wide), 4);
        assert_eq!(ring.push_output(wide, bytes(b"wide output")), 5);
        assert_eq!(ring.finish(Some(0)), 6);

        let stream = ring.to_stream();
        let sequences: Vec<TerminalSeq> = stream.iter().map(|e| e.seq()).collect();
        assert_eq!(sequences, vec![1, 2, 3, 4, 5, 6]);
        // Geometry travels with the events.
        assert_eq!(stream[4].size(), Some(wide));
        assert!(ring.is_finished());
        assert_eq!(ring.exit_code(), Some(Some(0)));
    }

    #[test]
    fn eviction_drops_oldest_events_and_keeps_geometry() {
        let ring = ReplayRing::new(TerminalSize::new(10, 3), 100);
        let small = TerminalSize::new(10, 3);
        for _ in 0..10 {
            ring.push_output(small, bytes(&[b'a'; 30]));
        }
        assert!(ring.total_bytes() <= 100);
        // The initial anchor may be evicted; the oldest surviving event
        // still carries its geometry.
        assert!(ring.first_size().is_some());
        assert!(ring.first_seq().unwrap() > 1);
        let stream = ring.to_stream();
        assert!(
            stream.iter().map(|e| e.output_bytes()).sum::<usize>() < ring.total_bytes(),
            "metadata must be included in the hard budget"
        );
        // Newest event always survives.
        assert_eq!(stream.last().unwrap().seq(), ring.last_seq());
    }

    #[test]
    fn single_oversized_event_is_trimmed_from_the_front() {
        let ring = ReplayRing::new(TerminalSize::new(10, 3), 100);
        let small = TerminalSize::new(10, 3);
        ring.push_output(small, bytes(&[b'x'; 500]));
        assert_eq!(ring.total_bytes(), 100);
        let stream = ring.to_stream();
        // The anchor is evicted first; the output survives as its tail and
        // still carries its geometry.
        assert_eq!(stream.len(), 1);
        assert_eq!(stream[0].output_bytes(), 100 - EVENT_OVERHEAD_BYTES);
        assert_eq!(stream[0].size(), Some(small));
    }

    #[test]
    fn resize_only_storm_is_memory_bounded() {
        let ring = ReplayRing::new(TerminalSize::new(10, 3), 1024);
        for columns in 20..20_000 {
            ring.push_resize(TerminalSize::new(columns, 40));
        }
        assert!(ring.total_bytes() <= 1024);
        assert!(ring.event_count() <= 1024 / EVENT_OVERHEAD_BYTES);
        assert_eq!(ring.last_size(), Some(TerminalSize::new(19_999, 40)));
    }

    #[test]
    fn trim_to_bounds_retired_history() {
        let ring = ReplayRing::new(TerminalSize::new(10, 3), 64 * 1024);
        let small = TerminalSize::new(10, 3);
        for _ in 0..20 {
            ring.push_output(small, bytes(&[b'y'; 1024]));
        }
        let before = ring.total_bytes();
        assert!(before >= 20 * 1024);
        ring.trim_to(4 * 1024);
        assert!(ring.total_bytes() <= 4 * 1024);
        assert!(ring.last_seq() > 1);
    }
}
