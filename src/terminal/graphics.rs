//! Local terminal graphics decoding.
//!
//! The PTY worker deliberately forwards graphics escapes as part of the raw
//! ordered stream. This module is used only by the GUI-side emulator, where
//! it turns the Kitty, iTerm2 inline-image, and Sixel protocols into bounded
//! RGBA image placements. The decoded data never crosses the control-plane
//! snapshot wire.

use std::collections::BTreeMap;
use std::sync::Arc;

use super::snapshot::{TerminalColor, TerminalSize, TerminalSnapshot};
use super::stream::decode_base64;

const MAX_GRAPHICS_BUFFER_BYTES: usize = 64 * 1024 * 1024;
const MAX_IMAGE_BYTES: usize = 32 * 1024 * 1024;
const MAX_IMAGE_PIXELS: u64 = 16 * 1024 * 1024;
const MAX_IMAGE_DIMENSION: u32 = 8 * 1024;
const MAX_STORED_IMAGE_BYTES: usize = 128 * 1024 * 1024;
const MAX_SIXEL_PIXELS: usize = if MAX_IMAGE_BYTES / 4 < MAX_IMAGE_PIXELS as usize {
    MAX_IMAGE_BYTES / 4
} else {
    MAX_IMAGE_PIXELS as usize
};
const IMAGE_OVERSCAN_ROWS: i32 = 64;

/// Kitty's Unicode placeholder code point. The actual image ID is carried by
/// the cell foreground color; the combining marks are only needed by a full
/// terminal implementation to preserve placeholder coordinates through host
/// applications such as tmux.
pub const TERMINAL_IMAGE_PLACEHOLDER: char = '\u{10EEEE}';

/// A decoded terminal image positioned in terminal-cell coordinates.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct TerminalImage {
    /// A GUI-local identity used by the render cache.
    pub id: u64,
    /// The row in the current terminal viewport coordinate system.
    pub row: i32,
    pub column: usize,
    pub width: usize,
    pub height: usize,
    pub pixel_width: u32,
    pub pixel_height: u32,
    /// Decoded RGBA pixels. This is shared between immutable snapshots.
    pub rgba: Arc<[u8]>,
}

#[derive(Debug)]
struct DecodedImage {
    width: u32,
    height: u32,
    rgba: Vec<u8>,
}

#[derive(Debug)]
struct ImagePlacement {
    line: i32,
    column: usize,
    width: usize,
    height: usize,
}

#[derive(Debug)]
struct ImageRecord {
    render_id: u64,
    width: u32,
    height: u32,
    rgba: Arc<[u8]>,
    placement: Option<ImagePlacement>,
    /// A Kitty `U=1` placement is anchored by placeholder cells rather than
    /// by the cursor. This stores the virtual placement's existence and lets
    /// the next terminal snapshot resolve its cell rectangle.
    placeholder_size: Option<(usize, usize)>,
}

#[derive(Debug)]
struct PendingKitty {
    encoded: Vec<u8>,
    parameters: Vec<u8>,
    action: u8,
}

/// GUI-local state for terminal graphics protocols.
#[derive(Debug, Default)]
pub struct TerminalGraphics {
    parser: GraphicsParser,
    images: BTreeMap<u32, ImageRecord>,
    pending_kitty: Option<(u32, PendingKitty)>,
    next_protocol_id: u32,
    next_render_id: u64,
    stored_bytes: usize,
}

impl TerminalGraphics {
    pub fn feed(&mut self, bytes: &[u8], cursor: (i32, usize)) -> Vec<Vec<u8>> {
        if bytes.is_empty() {
            return Vec::new();
        }

        // Full-screen TUI redraws conventionally erase terminal images too.
        // Kitty applications normally send an explicit delete action, while
        // this covers Sixel/iTerm2 producers that rely on the erase command.
        if bytes
            .windows(4)
            .any(|window| window == b"\x1b[2J" || window == b"\x1b[3J")
        {
            self.clear_images();
        }

        let mut responses = Vec::new();
        for event in self.parser.feed(bytes) {
            match event {
                GraphicsEvent::Kitty(payload) => {
                    if let Some(response) = self.handle_kitty(&payload, cursor) {
                        responses.push(response);
                    }
                }
                GraphicsEvent::Iterm(payload) => self.handle_iterm(&payload, cursor),
                GraphicsEvent::Sixel(payload) => self.handle_sixel(&payload, cursor),
            }
        }
        responses
    }

    /// Returns placements close enough to the current viewport to be painted.
    pub fn snapshot(
        &self,
        display_offset: usize,
        size: TerminalSize,
        terminal_snapshot: &TerminalSnapshot,
    ) -> Arc<[TerminalImage]> {
        let display_offset = display_offset as i32;
        let placeholder_bounds = self.placeholder_bounds(terminal_snapshot);
        self.images
            .values()
            .filter_map(|record| {
                let placement = if let Some((min_row, min_column, max_row, max_column)) =
                    placeholder_bounds.get(&record.render_id)
                {
                    Some(ImagePlacement {
                        line: *min_row,
                        column: *min_column,
                        width: max_column.saturating_sub(*min_column).saturating_add(1),
                        height: max_row.saturating_sub(*min_row).saturating_add(1) as usize,
                    })
                } else if record.placeholder_size.is_some() {
                    None
                } else {
                    record.placement.as_ref().map(|placement| ImagePlacement {
                        line: placement.line,
                        column: placement.column,
                        width: placement.width,
                        height: placement.height,
                    })
                }?;
                let row = placement.line.saturating_add(display_offset);
                let height = placement.height.max(1) as i32;
                if row.saturating_add(height) < -IMAGE_OVERSCAN_ROWS
                    || row > size.lines as i32 + IMAGE_OVERSCAN_ROWS
                {
                    return None;
                }
                Some(TerminalImage {
                    id: record.render_id,
                    row,
                    column: placement.column,
                    width: placement.width.max(1),
                    height: placement.height.max(1),
                    pixel_width: record.width,
                    pixel_height: record.height,
                    rgba: record.rgba.clone(),
                })
            })
            .collect::<Vec<_>>()
            .into()
    }

    /// Resolves Kitty Unicode placeholder cells from the local terminal
    /// snapshot. This is intentionally done after Alacritty has consumed the
    /// ordered stream, so cursor motion and SGR colors are already reflected
    /// in the cells. The resulting placement remains GUI-local.
    fn placeholder_bounds(
        &self,
        snapshot: &TerminalSnapshot,
    ) -> BTreeMap<u64, (i32, usize, i32, usize)> {
        let mut placeholder_bounds = BTreeMap::<u32, (i32, usize, i32, usize)>::new();
        for (row, cells) in snapshot.rows.iter().enumerate() {
            for (column, cell) in cells.iter().enumerate() {
                if cell.character != TERMINAL_IMAGE_PLACEHOLDER {
                    continue;
                }
                let Some(image_id) = placeholder_image_id(&cell.fg) else {
                    continue;
                };
                let Some(record) = self.images.get(&image_id) else {
                    continue;
                };
                if record.placeholder_size.is_none() {
                    continue;
                }
                let row = row as i32 - snapshot.display_offset as i32;
                placeholder_bounds
                    .entry(image_id)
                    .and_modify(|(min_row, min_column, max_row, max_column)| {
                        *min_row = (*min_row).min(row);
                        *min_column = (*min_column).min(column);
                        *max_row = (*max_row).max(row);
                        *max_column = (*max_column).max(column);
                    })
                    .or_insert((row, column, row, column));
            }
        }
        placeholder_bounds
            .into_iter()
            .filter_map(|(image_id, bounds)| {
                self.images
                    .get(&image_id)
                    .map(|record| (record.render_id, bounds))
            })
            .collect()
    }

    fn handle_kitty(&mut self, payload: &[u8], cursor: (i32, usize)) -> Option<Vec<u8>> {
        let (parameters, data) = split_once_byte(payload, b';').unwrap_or((payload, &[]));
        let action = parameter(parameters, b'a')
            .and_then(|value| value.first().copied())
            .unwrap_or(b't');

        if action == b'q' {
            let image_id = parameter_u32(parameters, b'i').filter(|id| *id != 0)?;
            return Some(format!("\x1b_Gi={image_id};OK\x1b\\").into_bytes());
        }
        if action == b'd' {
            self.delete_kitty(parameters);
            return None;
        }
        if action == b'p' {
            if let Some(image_id) = parameter_u32(parameters, b'i') {
                if parameter(parameters, b'U') == Some(b"1") {
                    if let Some(record) = self.images.get_mut(&image_id) {
                        record.placeholder_size = placeholder_size_from_parameters(parameters);
                        record.placement = None;
                    }
                } else {
                    self.place(image_id, parameters, cursor);
                }
            }
            return None;
        }
        if action != b'T' && action != b't' {
            return None;
        }

        // Kitty sends all chunks for one image consecutively. Continuation
        // chunks normally contain only `m=1`/`m=0`, so retain the first
        // chunk's ID and control data until the final chunk arrives.
        let explicit_id = parameter_u32(parameters, b'i').filter(|id| *id != 0);
        if explicit_id.is_some()
            && self
                .pending_kitty
                .as_ref()
                .is_some_and(|(pending_id, _)| Some(*pending_id) != explicit_id)
        {
            self.pending_kitty = None;
        }
        let image_id = explicit_id
            .or_else(|| self.pending_kitty.as_ref().map(|(id, _)| *id))
            .unwrap_or_else(|| self.allocate_protocol_id());
        if self
            .pending_kitty
            .as_ref()
            .is_none_or(|(pending_id, _)| *pending_id != image_id)
        {
            self.pending_kitty = Some((
                image_id,
                PendingKitty {
                    encoded: Vec::new(),
                    parameters: parameters.to_vec(),
                    action,
                },
            ));
        }
        let Some((_, pending)) = self.pending_kitty.as_mut() else {
            return None;
        };
        if pending.encoded.len().saturating_add(data.len()) > MAX_GRAPHICS_BUFFER_BYTES {
            self.pending_kitty = None;
            return None;
        }
        pending.encoded.extend_from_slice(data);
        if parameter_u32(parameters, b'm') == Some(1) {
            return None;
        }
        let Some((image_id, pending)) = self.pending_kitty.take() else {
            return None;
        };
        let Some(encoded) = std::str::from_utf8(&pending.encoded).ok() else {
            return None;
        };
        let Some(raw) = decode_base64(encoded) else {
            return None;
        };
        let format = parameter_u32(&pending.parameters, b'f').unwrap_or(100);
        let Some(decoded) = self.decode_kitty_data(&raw, format, &pending.parameters) else {
            return None;
        };
        self.store(
            image_id,
            decoded,
            (pending.action == b'T' && parameter_u32(&pending.parameters, b'U') != Some(1))
                .then(|| placement_from_parameters(&pending.parameters, cursor)),
            (pending.action == b'T' && parameter_u32(&pending.parameters, b'U') == Some(1))
                .then(|| placeholder_size_from_parameters(&pending.parameters))
                .flatten(),
        );
        None
    }

    fn decode_kitty_data(
        &self,
        data: &[u8],
        format: u32,
        parameters: &[u8],
    ) -> Option<DecodedImage> {
        if parameter(parameters, b't') == Some(b"f") {
            let path = std::str::from_utf8(data).ok()?;
            let metadata = std::fs::metadata(path).ok()?;
            let length = usize::try_from(metadata.len()).ok()?;
            if length > MAX_IMAGE_BYTES {
                return None;
            }
            return decode_encoded_image(&std::fs::read(path).ok()?);
        }
        match format {
            24 => decode_raw_image(
                data,
                parameter_u32(parameters, b's')?,
                parameter_u32(parameters, b'v')?,
                3,
            ),
            32 => decode_raw_image(
                data,
                parameter_u32(parameters, b's')?,
                parameter_u32(parameters, b'v')?,
                4,
            ),
            _ => decode_encoded_image(data),
        }
    }

    fn handle_iterm(&mut self, payload: &[u8], cursor: (i32, usize)) {
        let Some(rest) = payload.strip_prefix(b"1337;File=") else {
            return;
        };
        let Some((options, encoded)) = split_once_byte(rest, b':') else {
            return;
        };
        let options = parse_semicolon_options(options);
        let inline = options
            .get(b"inline".as_slice())
            .is_some_and(|value| *value == b"1");
        if !inline {
            return;
        }
        let Some(encoded) = std::str::from_utf8(encoded).ok() else {
            return;
        };
        let Some(raw) = decode_base64(encoded) else {
            return;
        };
        let Some(decoded) = decode_encoded_image(&raw) else {
            return;
        };
        let width = options
            .get(b"width".as_slice())
            .and_then(|value| parse_dimension(value));
        let height = options
            .get(b"height".as_slice())
            .and_then(|value| parse_dimension(value));
        let mut placement =
            placement_from_dimensions(cursor, decoded.width, decoded.height, width, height);
        if options
            .get(b"preserveAspectRatio".as_slice())
            .is_some_and(|value| *value == b"0")
        {
            placement.width = width.unwrap_or(placement.width).max(1);
            placement.height = height.unwrap_or(placement.height).max(1);
        }
        let image_id = self.allocate_protocol_id();
        self.store(image_id, decoded, Some(placement), None);
    }

    fn handle_sixel(&mut self, payload: &[u8], cursor: (i32, usize)) {
        let Some(decoded) = decode_sixel(payload) else {
            return;
        };
        let image_id = self.allocate_protocol_id();
        let placement =
            placement_from_dimensions(cursor, decoded.width, decoded.height, None, None);
        self.store(image_id, decoded, Some(placement), None);
    }

    fn store(
        &mut self,
        image_id: u32,
        decoded: DecodedImage,
        placement: Option<ImagePlacement>,
        placeholder_size: Option<(usize, usize)>,
    ) {
        let byte_len = decoded.rgba.len();
        let previous_bytes = self
            .images
            .get(&image_id)
            .map_or(0, |record| record.rgba.len());
        let stored_without_previous = self.stored_bytes.saturating_sub(previous_bytes);
        if byte_len == 0
            || byte_len > MAX_IMAGE_BYTES
            || byte_len > MAX_STORED_IMAGE_BYTES.saturating_sub(stored_without_previous)
        {
            return;
        }
        if let Some(previous) = self.images.remove(&image_id) {
            self.stored_bytes = self.stored_bytes.saturating_sub(previous.rgba.len());
        }
        let render_id = self.next_render_id;
        self.next_render_id = self.next_render_id.wrapping_add(1).max(1);
        self.stored_bytes = self.stored_bytes.saturating_add(byte_len);
        self.images.insert(
            image_id,
            ImageRecord {
                render_id,
                width: decoded.width,
                height: decoded.height,
                rgba: decoded.rgba.into(),
                placement,
                placeholder_size,
            },
        );
    }

    fn place(&mut self, image_id: u32, parameters: &[u8], cursor: (i32, usize)) {
        let Some(record) = self.images.get_mut(&image_id) else {
            return;
        };
        record.placement = Some(placement_from_dimensions(
            cursor,
            record.width,
            record.height,
            parameter_u32(parameters, b'c').and_then(|value| usize::try_from(value).ok()),
            parameter_u32(parameters, b'r').and_then(|value| usize::try_from(value).ok()),
        ));
    }

    fn delete_kitty(&mut self, parameters: &[u8]) {
        match parameter(parameters, b'd') {
            Some(b"a") => self.clear_images(),
            Some(b"A") => self.clear_images(),
            Some(b"i") | Some(b"I") => {
                if let Some(image_id) = parameter_u32(parameters, b'i') {
                    if let Some(record) = self.images.remove(&image_id) {
                        self.stored_bytes = self.stored_bytes.saturating_sub(record.rgba.len());
                    }
                }
            }
            _ => {}
        }
    }

    fn clear_images(&mut self) {
        self.images.clear();
        self.pending_kitty = None;
        self.stored_bytes = 0;
    }

    fn allocate_protocol_id(&mut self) -> u32 {
        loop {
            self.next_protocol_id = self.next_protocol_id.wrapping_add(1).max(1);
            if !self.images.contains_key(&self.next_protocol_id)
                && self
                    .pending_kitty
                    .as_ref()
                    .is_none_or(|(id, _)| *id != self.next_protocol_id)
            {
                return self.next_protocol_id;
            }
        }
    }
}

#[derive(Debug, Default)]
struct GraphicsParser {
    buffer: Vec<u8>,
}

#[derive(Debug)]
enum GraphicsEvent {
    Kitty(Vec<u8>),
    Iterm(Vec<u8>),
    Sixel(Vec<u8>),
}

#[derive(Debug, Clone, Copy)]
enum GraphicsKind {
    Kitty,
    Iterm,
    Sixel,
}

impl GraphicsParser {
    fn feed(&mut self, bytes: &[u8]) -> Vec<GraphicsEvent> {
        self.buffer.extend_from_slice(bytes);
        let mut events = Vec::new();
        loop {
            let Some((start, kind)) = find_graphics_start(&self.buffer) else {
                // Keep enough UTF-8 context to distinguish a C1 control from
                // a continuation byte when the text stream splits a scalar.
                let retain = self.buffer.len().min(3);
                let split_at = self.buffer.len().saturating_sub(retain);
                self.buffer.drain(..split_at);
                break;
            };
            if start > 0 {
                self.buffer.drain(..start);
            }
            let header_len = match kind {
                GraphicsKind::Kitty => self
                    .buffer
                    .starts_with(&[0x9f, b'G'])
                    .then_some(2)
                    .unwrap_or(3),
                GraphicsKind::Iterm => {
                    if self.buffer.first() == Some(&0x9d) {
                        1
                    } else {
                        2
                    }
                }
                GraphicsKind::Sixel => {
                    if self.buffer.first() == Some(&0x90) {
                        1
                    } else {
                        2
                    }
                }
            };
            let Some((terminator, terminator_len)) = find_graphics_terminator(
                &self.buffer[header_len..],
                matches!(kind, GraphicsKind::Iterm),
            ) else {
                if self.buffer.len() > MAX_GRAPHICS_BUFFER_BYTES {
                    self.buffer.clear();
                }
                break;
            };
            let payload_start = header_len;
            let payload_end = header_len + terminator;
            let payload = self.buffer[payload_start..payload_end].to_vec();
            self.buffer.drain(..payload_end + terminator_len);
            events.push(match kind {
                GraphicsKind::Kitty => GraphicsEvent::Kitty(payload),
                GraphicsKind::Iterm => GraphicsEvent::Iterm(payload),
                GraphicsKind::Sixel => GraphicsEvent::Sixel(payload),
            });
        }
        events
    }
}

fn find_graphics_start(bytes: &[u8]) -> Option<(usize, GraphicsKind)> {
    for index in 0..bytes.len() {
        if bytes[index] != 0x1b {
            if !is_utf8_continuation(bytes, index)
                && bytes[index] == 0x9f
                && bytes.get(index + 1) == Some(&b'G')
            {
                return Some((index, GraphicsKind::Kitty));
            }
            if !is_utf8_continuation(bytes, index) && bytes[index] == 0x9d {
                return Some((index, GraphicsKind::Iterm));
            }
            if !is_utf8_continuation(bytes, index) && bytes[index] == 0x90 {
                return Some((index, GraphicsKind::Sixel));
            }
            continue;
        }
        if bytes.get(index + 1) == Some(&b'_') && bytes.get(index + 2) == Some(&b'G') {
            return Some((index, GraphicsKind::Kitty));
        }
        if bytes.get(index + 1) == Some(&b']') {
            return Some((index, GraphicsKind::Iterm));
        }
        if bytes.get(index + 1) == Some(&b'P') {
            return Some((index, GraphicsKind::Sixel));
        }
    }
    None
}

fn is_utf8_continuation(bytes: &[u8], index: usize) -> bool {
    if !(0x80..=0xbf).contains(&bytes[index]) {
        return false;
    }
    let mut lead = index;
    let mut preceding_continuations = 0;
    while lead > 0 && preceding_continuations < 3 && (bytes[lead - 1] & 0xc0) == 0x80 {
        lead -= 1;
        preceding_continuations += 1;
    }
    let required_continuations = match bytes.get(lead).copied() {
        Some(0xc2..=0xdf) => 1,
        Some(0xe0..=0xef) => 2,
        Some(0xf0..=0xf4) => 3,
        _ => return false,
    };
    preceding_continuations < required_continuations
}

fn find_graphics_terminator(bytes: &[u8], allow_bel: bool) -> Option<(usize, usize)> {
    for index in 0..bytes.len() {
        if allow_bel && bytes[index] == 0x07 {
            return Some((index, 1));
        }
        if bytes[index] == 0x1b && bytes.get(index + 1) == Some(&b'\\') {
            return Some((index, 2));
        }
        if bytes[index] == 0x9c {
            return Some((index, 1));
        }
    }
    None
}

fn parameter<'a>(parameters: &'a [u8], key: u8) -> Option<&'a [u8]> {
    parameters.split(|byte| *byte == b',').find_map(|part| {
        let (name, value) = split_once_byte(part, b'=')?;
        (name == [key]).then_some(value)
    })
}

fn parameter_u32(parameters: &[u8], key: u8) -> Option<u32> {
    parameter(parameters, key)?
        .iter()
        .try_fold(0u32, |value, byte| {
            value
                .checked_mul(10)?
                .checked_add(u32::from(byte.checked_sub(b'0')?))
        })
}

fn parse_semicolon_options(options: &[u8]) -> BTreeMap<&[u8], &[u8]> {
    options
        .split(|byte| *byte == b';')
        .filter_map(|part| {
            let (name, value) = split_once_byte(part, b'=')?;
            Some((name, value))
        })
        .collect()
}

fn split_once_byte(bytes: &[u8], separator: u8) -> Option<(&[u8], &[u8])> {
    let index = bytes.iter().position(|byte| *byte == separator)?;
    Some((&bytes[..index], &bytes[index + 1..]))
}

fn parse_dimension(value: &[u8]) -> Option<usize> {
    let value = value
        .strip_suffix(b"px")
        .or_else(|| value.strip_suffix(b"%"))
        .unwrap_or(value);
    std::str::from_utf8(value).ok()?.parse().ok()
}

fn placeholder_size_from_parameters(parameters: &[u8]) -> Option<(usize, usize)> {
    let columns = parameter_u32(parameters, b'c')
        .and_then(|value| usize::try_from(value).ok())
        .filter(|value| *value > 0)?;
    let rows = parameter_u32(parameters, b'r')
        .and_then(|value| usize::try_from(value).ok())
        .filter(|value| *value > 0)?;
    Some((columns, rows))
}

fn placeholder_image_id(color: &TerminalColor) -> Option<u32> {
    match color {
        TerminalColor::Named { value } => Some(u32::from(*value)).filter(|value| *value > 0),
        TerminalColor::Indexed { value } => Some(u32::from(*value)).filter(|value| *value > 0),
        TerminalColor::Rgb { red, green, blue } => {
            Some((u32::from(*red) << 16) | (u32::from(*green) << 8) | u32::from(*blue))
                .filter(|value| *value > 0)
        }
    }
}

fn decode_encoded_image(data: &[u8]) -> Option<DecodedImage> {
    if data.len() > MAX_IMAGE_BYTES {
        return None;
    }
    let image = image::load_from_memory(data).ok()?.to_rgba8();
    let (width, height) = image.dimensions();
    validate_image_size(width, height, image.as_raw().len())?;
    Some(DecodedImage {
        width,
        height,
        rgba: image.into_raw(),
    })
}

fn decode_raw_image(data: &[u8], width: u32, height: u32, channels: usize) -> Option<DecodedImage> {
    let pixels = usize::try_from(width)
        .ok()?
        .checked_mul(usize::try_from(height).ok()?)?;
    let expected = pixels.checked_mul(channels)?;
    validate_image_size(width, height, expected)?;
    if data.len() < expected {
        return None;
    }
    let mut rgba = Vec::with_capacity(pixels * 4);
    for pixel in data[..expected].chunks_exact(channels) {
        rgba.extend_from_slice(&pixel[..3]);
        if channels == 4 {
            rgba.push(pixel[3]);
        } else {
            rgba.push(255);
        }
    }
    Some(DecodedImage {
        width,
        height,
        rgba,
    })
}

fn validate_image_size(width: u32, height: u32, bytes: usize) -> Option<()> {
    (width > 0
        && height > 0
        && width <= MAX_IMAGE_DIMENSION
        && height <= MAX_IMAGE_DIMENSION
        && u64::from(width).checked_mul(u64::from(height))? <= MAX_IMAGE_PIXELS
        && bytes <= MAX_IMAGE_BYTES)
        .then_some(())
}

fn placement_from_parameters(parameters: &[u8], cursor: (i32, usize)) -> ImagePlacement {
    placement_from_dimensions(
        cursor,
        parameter_u32(parameters, b's').unwrap_or(0),
        parameter_u32(parameters, b'v').unwrap_or(0),
        parameter_u32(parameters, b'c').and_then(|value| usize::try_from(value).ok()),
        parameter_u32(parameters, b'r').and_then(|value| usize::try_from(value).ok()),
    )
}

fn placement_from_dimensions(
    cursor: (i32, usize),
    pixel_width: u32,
    pixel_height: u32,
    requested_width: Option<usize>,
    requested_height: Option<usize>,
) -> ImagePlacement {
    let width = requested_width
        .filter(|value| *value > 0)
        .unwrap_or_else(|| {
            (usize::try_from(pixel_width).unwrap_or(1).saturating_add(7) / 8).max(1)
        });
    let height = requested_height
        .filter(|value| *value > 0)
        .unwrap_or_else(|| {
            let numerator = u64::from(pixel_height)
                .saturating_mul(width as u64)
                .saturating_add(
                    u64::from(pixel_width.max(1))
                        .saturating_mul(2)
                        .saturating_sub(1),
                );
            usize::try_from(numerator / u64::from(pixel_width.max(1)) / 2)
                .unwrap_or(1)
                .max(1)
        });
    ImagePlacement {
        line: cursor.0,
        column: cursor.1,
        width: width.max(1),
        height: height.max(1),
    }
}

fn decode_sixel(payload: &[u8]) -> Option<DecodedImage> {
    let start = payload.iter().position(|byte| *byte == b'q')? + 1;
    let mut canvas = SixelCanvas::default();
    let mut colors = [[255u8, 255, 255, 255]; 256];
    let mut color_index = 0usize;
    let mut x = 0usize;
    let mut y = 0usize;
    let mut index = start;
    while index < payload.len() {
        match payload[index] {
            b'"' => {
                let (values, next) = sixel_numbers(&payload[index + 1..]);
                if let Some(width) = values.get(2).copied().filter(|value| *value > 0) {
                    canvas.ensure(width as usize, values.get(3).copied().unwrap_or(0) as usize);
                }
                index = index.saturating_add(next + 1);
            }
            b'#' => {
                let (number, mut next) = sixel_number(&payload[index + 1..]);
                color_index = number.unwrap_or(0).min(255) as usize;
                if payload.get(index + 1 + next) == Some(&b';') {
                    let (values, consumed) = sixel_numbers(&payload[index + 2 + next..]);
                    if values.first() == Some(&2) && values.len() >= 4 {
                        colors[color_index] = [
                            ((values[1].min(100) * 255) / 100) as u8,
                            ((values[2].min(100) * 255) / 100) as u8,
                            ((values[3].min(100) * 255) / 100) as u8,
                            255,
                        ];
                    }
                    next = next.saturating_add(consumed + 1);
                }
                index = index.saturating_add(next + 1);
            }
            b'!' => {
                let (repeat, consumed) = sixel_number(&payload[index + 1..]);
                let repeat = repeat.unwrap_or(1).min(4096) as usize;
                let Some(&character) = payload.get(index + consumed + 1) else {
                    break;
                };
                if (b'?'..=b'~').contains(&character) {
                    for _ in 0..repeat {
                        draw_sixel(&mut canvas, &mut x, y, character, colors[color_index]);
                    }
                }
                index = index.saturating_add(consumed + 2);
            }
            b'$' => {
                x = 0;
                index += 1;
            }
            b'-' => {
                x = 0;
                y = y.saturating_add(6);
                index += 1;
            }
            character if (b'?'..=b'~').contains(&character) => {
                draw_sixel(&mut canvas, &mut x, y, character, colors[color_index]);
                index += 1;
            }
            _ => index += 1,
        }
    }
    canvas.finish()
}

#[derive(Debug, Default)]
struct SixelCanvas {
    width: usize,
    height: usize,
    pixels: Vec<u8>,
    max_x: usize,
    max_y: usize,
}

impl SixelCanvas {
    fn ensure(&mut self, required_width: usize, required_height: usize) {
        let required_width = required_width.max(1).min(MAX_IMAGE_DIMENSION as usize);
        let required_height = required_height.max(1).min(MAX_IMAGE_DIMENSION as usize);
        if required_width <= self.width && required_height <= self.height {
            return;
        }
        let width = self
            .width
            .max(required_width)
            .max(self.width.saturating_mul(2))
            .max(1)
            .min(MAX_IMAGE_DIMENSION as usize);
        let height = self
            .height
            .max(required_height)
            .max(self.height.saturating_mul(2))
            .max(1)
            .min(MAX_IMAGE_DIMENSION as usize);
        if width.saturating_mul(height) > MAX_SIXEL_PIXELS {
            return;
        }
        let mut pixels = vec![0; width.saturating_mul(height).saturating_mul(4)];
        for row in 0..self.height.min(height) {
            let old_start = row * self.width * 4;
            let new_start = row * width * 4;
            let length = self.width.min(width) * 4;
            pixels[new_start..new_start + length]
                .copy_from_slice(&self.pixels[old_start..old_start + length]);
        }
        self.width = width;
        self.height = height;
        self.pixels = pixels;
    }

    fn set(&mut self, x: usize, y: usize, color: [u8; 4]) {
        if x >= MAX_IMAGE_DIMENSION as usize || y >= MAX_IMAGE_DIMENSION as usize {
            return;
        }
        self.ensure(x + 1, y + 1);
        if x >= self.width || y >= self.height {
            return;
        }
        let offset = (y * self.width + x) * 4;
        self.pixels[offset..offset + 4].copy_from_slice(&color);
        self.max_x = self.max_x.max(x + 1);
        self.max_y = self.max_y.max(y + 1);
    }

    fn finish(self) -> Option<DecodedImage> {
        if self.max_x == 0 || self.max_y == 0 {
            return None;
        }
        let mut rgba = Vec::with_capacity(self.max_x * self.max_y * 4);
        for row in 0..self.max_y {
            let start = row * self.width * 4;
            rgba.extend_from_slice(&self.pixels[start..start + self.max_x * 4]);
        }
        validate_image_size(self.max_x as u32, self.max_y as u32, rgba.len())?;
        Some(DecodedImage {
            width: self.max_x as u32,
            height: self.max_y as u32,
            rgba,
        })
    }
}

fn draw_sixel(canvas: &mut SixelCanvas, x: &mut usize, y: usize, character: u8, color: [u8; 4]) {
    let bits = character.saturating_sub(b'?');
    for bit in 0..6 {
        if bits & (1 << bit) != 0 {
            canvas.set(*x, y.saturating_add(bit), color);
        }
    }
    *x = x.saturating_add(1);
}

fn sixel_number(bytes: &[u8]) -> (Option<u32>, usize) {
    let mut value = 0u32;
    let mut consumed = 0;
    while let Some(byte) = bytes.get(consumed).copied() {
        if !byte.is_ascii_digit() {
            break;
        }
        value = value
            .saturating_mul(10)
            .saturating_add(u32::from(byte - b'0'));
        consumed += 1;
    }
    (Some(value).filter(|_| consumed > 0), consumed)
}

fn sixel_numbers(bytes: &[u8]) -> (Vec<u32>, usize) {
    let mut values = Vec::new();
    let mut consumed = 0;
    loop {
        let (value, length) = sixel_number(&bytes[consumed..]);
        let Some(value) = value else {
            break;
        };
        values.push(value);
        consumed += length;
        if bytes.get(consumed) != Some(&b';') {
            break;
        }
        consumed += 1;
    }
    (values, consumed)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::ids::TerminalId;

    #[test]
    fn kitty_graphics_survives_split_escape_sequence() {
        let mut parser = GraphicsParser::default();
        assert!(parser.feed(b"prefix\x1b_Ga=T;").is_empty());
        let mut second = b"data\x1b".to_vec();
        second.push(92);
        second.extend_from_slice(b"suffix");
        let events = parser.feed(&second);
        assert!(
            matches!(events.as_slice(), [GraphicsEvent::Kitty(payload)] if payload == b"a=T;data")
        );
    }

    #[test]
    fn c1_graphics_sequences_are_supported() {
        let mut parser = GraphicsParser::default();
        let events = parser.feed(b"\x9fGi=31,a=q\x9c");
        assert!(
            matches!(events.as_slice(), [GraphicsEvent::Kitty(payload)] if payload == b"i=31,a=q")
        );

        let mut parser = GraphicsParser::default();
        assert!(parser.feed("”".as_bytes()).is_empty());
    }

    #[test]
    fn sixel_decodes_pixels_and_palette() {
        let decoded = decode_sixel(b"q#1;2;100;0;0~").unwrap();
        assert_eq!((decoded.width, decoded.height), (1, 6));
        assert_eq!(&decoded.rgba[..4], &[255, 0, 0, 255]);
    }

    #[test]
    fn kitty_png_is_placed_at_cursor() {
        let png = image::RgbaImage::from_pixel(2, 2, image::Rgba([1, 2, 3, 255]));
        let mut bytes = Vec::new();
        let encoder = image::codecs::png::PngEncoder::new(&mut bytes);
        image::ImageEncoder::write_image(
            encoder,
            png.as_raw(),
            2,
            2,
            image::ExtendedColorType::Rgba8,
        )
        .unwrap();
        let encoded = super::super::stream::encode_base64(&bytes);
        let mut graphics = TerminalGraphics::default();
        graphics.feed(
            format!("\x1b_Ga=T,f=100,c=2,r=2;{encoded}\x1b\\").as_bytes(),
            (3, 4),
        );
        let terminal_snapshot =
            TerminalSnapshot::empty(TerminalId::new(1), TerminalSize::new(80, 24));
        let snapshot = graphics.snapshot(0, TerminalSize::new(80, 24), &terminal_snapshot);
        assert_eq!(snapshot.len(), 1);
        assert_eq!((snapshot[0].row, snapshot[0].column), (3, 4));
        assert_eq!((snapshot[0].pixel_width, snapshot[0].pixel_height), (2, 2));
    }

    #[test]
    fn kitty_chunked_unicode_placeholder_is_resolved_from_terminal_cells() {
        let png = image::RgbaImage::from_pixel(2, 2, image::Rgba([1, 2, 3, 255]));
        let mut bytes = Vec::new();
        let encoder = image::codecs::png::PngEncoder::new(&mut bytes);
        image::ImageEncoder::write_image(
            encoder,
            png.as_raw(),
            2,
            2,
            image::ExtendedColorType::Rgba8,
        )
        .unwrap();
        let encoded = super::super::stream::encode_base64(&bytes);
        let split = (encoded.len() / 2) & !3;
        let mut graphics = TerminalGraphics::default();
        graphics.feed(
            format!(
                "\x1b_Gq=2,a=T,C=1,U=1,f=100,s=2,v=2,c=2,r=2,i=42,m=1;{}\x1b\\",
                &encoded[..split]
            )
            .as_bytes(),
            (0, 0),
        );
        graphics.feed(
            format!("\x1b_Gm=0;{}\x1b\\", &encoded[split..]).as_bytes(),
            (0, 0),
        );

        let size = TerminalSize::new(80, 24);
        let mut terminal_snapshot = TerminalSnapshot::empty(TerminalId::new(1), size);
        for row in 1..3 {
            for column in 5..7 {
                let cell = terminal_snapshot.cell_mut(row, column).unwrap();
                cell.character = TERMINAL_IMAGE_PLACEHOLDER;
                cell.fg = TerminalColor::Rgb {
                    red: 0,
                    green: 0,
                    blue: 42,
                };
            }
        }
        let snapshot = graphics.snapshot(0, size, &terminal_snapshot);
        assert_eq!(snapshot.len(), 1);
        assert_eq!((snapshot[0].row, snapshot[0].column), (1, 5));
        assert_eq!((snapshot[0].width, snapshot[0].height), (2, 2));
    }

    #[test]
    fn kitty_capability_query_is_answered() {
        let mut graphics = TerminalGraphics::default();
        let responses = graphics.feed(b"\x1b_Gi=278941603,s=1,v=1,a=q,t=d,f=24;AAAA\x1b\\", (0, 0));
        assert_eq!(responses, vec![b"\x1b_Gi=278941603;OK\x1b\\".to_vec()]);
    }
}
