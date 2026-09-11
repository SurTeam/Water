//! Ordered raw PTY stream events.
//!
//! The server owns PTY output; it never renders it. Every terminal produces
//! a strictly ordered stream of `Output`, `Resize`, and `Exit` events with a
//! monotonically increasing sequence. The GUI (and any replay consumer)
//! rebuilds its local Alacritty emulator by applying the events in order:
//! historical replay first, then the live tail. Clients deduplicate by
//! sequence, so an overlap between the replay and the live tail is safe.

use std::sync::Arc;

use serde::{Deserialize, Serialize};

use super::snapshot::TerminalSize;

/// Monotonic per-terminal sequence assigned by the PTY worker.
pub type TerminalSeq = u64;

/// One ordered raw event on a terminal's stream.
///
/// `size` is the geometry the event was produced at; it is what makes
/// eviction of the oldest ring events safe (every surviving event still
/// carries its geometry context).
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum TerminalStreamEvent {
    Output {
        seq: TerminalSeq,
        size: TerminalSize,
        bytes: Arc<[u8]>,
    },
    Resize {
        seq: TerminalSeq,
        size: TerminalSize,
    },
    Exit {
        seq: TerminalSeq,
        code: Option<i32>,
    },
}

impl TerminalStreamEvent {
    pub fn seq(&self) -> TerminalSeq {
        match self {
            Self::Output { seq, .. } | Self::Resize { seq, .. } | Self::Exit { seq, .. } => *seq,
        }
    }

    pub fn size(&self) -> Option<TerminalSize> {
        match self {
            Self::Output { size, .. } | Self::Resize { size, .. } => Some(*size),
            Self::Exit { .. } => None,
        }
    }

    /// Raw output byte length (0 for Resize/Exit).
    pub fn output_bytes(&self) -> usize {
        match self {
            Self::Output { bytes, .. } => bytes.len(),
            _ => 0,
        }
    }

    /// Wire form: base64 output bytes, geometry only where it matters.
    pub fn to_wire(&self) -> WireTerminalEvent {
        match self {
            Self::Output { seq, bytes, .. } => WireTerminalEvent::Output {
                seq: *seq,
                bytes: encode_base64(bytes),
            },
            Self::Resize { seq, size } => WireTerminalEvent::Resize {
                seq: *seq,
                columns: size.columns,
                lines: size.lines,
            },
            Self::Exit { seq, code } => WireTerminalEvent::Exit {
                seq: *seq,
                code: *code,
            },
        }
    }

    /// Inverse of [`to_wire`]. Returns `None` when the geometry (carried by
    /// the stream frame or an earlier event) is unavailable.
    pub fn from_wire(wire: &WireTerminalEvent, fallback_size: TerminalSize) -> Option<Self> {
        match wire {
            WireTerminalEvent::Output { seq, bytes } => {
                decode_base64(bytes).map(|bytes| Self::Output {
                    seq: *seq,
                    size: fallback_size,
                    bytes: Arc::from(bytes),
                })
            }
            WireTerminalEvent::Resize {
                seq,
                columns,
                lines,
            } => Some(Self::Resize {
                seq: *seq,
                size: TerminalSize::new(*columns, *lines),
            }),
            WireTerminalEvent::Exit { seq, code } => Some(Self::Exit {
                seq: *seq,
                code: *code,
            }),
        }
    }
}

/// Wire form of a stream event (JSON-friendly: base64 bytes).
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "type", rename_all = "snake_case")]
pub enum WireTerminalEvent {
    Output {
        seq: TerminalSeq,
        bytes: String,
    },
    Resize {
        seq: TerminalSeq,
        columns: usize,
        lines: usize,
    },
    Exit {
        seq: TerminalSeq,
        code: Option<i32>,
    },
}

const B64_ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

pub fn encode_base64(bytes: &[u8]) -> String {
    let out_len = bytes.len().div_ceil(3) * 4;
    let mut out = vec![0u8; out_len];
    let alpha = B64_ALPHABET;
    let mut i = 0;
    let mut o = 0;
    // Process 3-byte chunks in a tight loop (avoids per-char push overhead)
    while i + 3 <= bytes.len() {
        let triple =
            (bytes[i] as u32) << 16 | (bytes[i + 1] as u32) << 8 | bytes[i + 2] as u32;
        out[o] = alpha[(triple >> 18) as usize & 63];
        out[o + 1] = alpha[(triple >> 12) as usize & 63];
        out[o + 2] = alpha[(triple >> 6) as usize & 63];
        out[o + 3] = alpha[triple as usize & 63];
        i += 3;
        o += 4;
    }
    // Handle remaining 1-2 bytes
    match bytes.len() - i {
        1 => {
            let triple = (bytes[i] as u32) << 16;
            out[o] = alpha[(triple >> 18) as usize & 63];
            out[o + 1] = alpha[(triple >> 12) as usize & 63];
            out[o + 2] = b'=';
            out[o + 3] = b'=';
        }
        2 => {
            let triple = (bytes[i] as u32) << 16 | (bytes[i + 1] as u32) << 8;
            out[o] = alpha[(triple >> 18) as usize & 63];
            out[o + 1] = alpha[(triple >> 12) as usize & 63];
            out[o + 2] = alpha[(triple >> 6) as usize & 63];
            out[o + 3] = b'=';
        }
        _ => {}
    }
    // Base64 output is always valid ASCII/UTF-8
    unsafe { String::from_utf8_unchecked(out) }
}

fn b64_value(byte: u8) -> Option<u32> {
    match byte {
        b'A'..=b'Z' => Some(u32::from(byte - b'A')),
        b'a'..=b'z' => Some(u32::from(byte - b'a') + 26),
        b'0'..=b'9' => Some(u32::from(byte - b'0') + 52),
        b'+' => Some(62),
        b'/' => Some(63),
        _ => None,
    }
}

pub fn decode_base64(encoded: &str) -> Option<Vec<u8>> {
    let bytes: Vec<u8> = encoded
        .bytes()
        .filter(|byte| !byte.is_ascii_whitespace())
        .collect();
    let significant = bytes.iter().take_while(|byte| **byte != b'=').count();
    let mut out = Vec::with_capacity(significant * 3 / 4 + 3);
    let mut buffer: u32 = 0;
    let mut bits = 0;
    for &byte in &bytes {
        if byte == b'=' {
            continue;
        }
        let value = b64_value(byte)?;
        buffer = (buffer << 6) | value as u32;
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((buffer >> bits) as u8);
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64_roundtrips_all_remainder_lengths() {
        for length in 0..8_u8 {
            let data: Vec<u8> = (0..length).map(|i| (i * 37 + 11) as u8).collect();
            let encoded = encode_base64(&data);
            assert_eq!(decode_base64(&encoded).as_deref(), Some(&data[..]));
        }
        let data: Vec<u8> = (0..257u16).map(|i| (i % 251) as u8).collect();
        let encoded = encode_base64(&data);
        assert_eq!(decode_base64(&encoded).as_deref(), Some(&data[..]));
    }

    #[test]
    fn wire_events_roundtrip_with_geometry_fallback() {
        let size = TerminalSize::new(100, 30);
        let events = [
            TerminalStreamEvent::Resize { seq: 1, size },
            TerminalStreamEvent::Output {
                seq: 2,
                size,
                bytes: Arc::from(vec![0, 1, 2, 3, 255]),
            },
            TerminalStreamEvent::Exit {
                seq: 3,
                code: Some(7),
            },
        ];
        for event in &events {
            let wire = event.to_wire();
            let json = serde_json::to_string(&wire).unwrap();
            let parsed: WireTerminalEvent = serde_json::from_str(&json).unwrap();
            assert_eq!(
                TerminalStreamEvent::from_wire(&parsed, size).as_ref(),
                Some(event)
            );
        }
        let output_json = serde_json::json!({ "type": "output", "seq": 4, "bytes": "aGk=" });
        let parsed: WireTerminalEvent = serde_json::from_value(output_json).unwrap();
        let event = TerminalStreamEvent::from_wire(&parsed, size).unwrap();
        assert_eq!(event.output_bytes(), 2);
    }
}
