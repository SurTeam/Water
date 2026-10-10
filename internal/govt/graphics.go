package govt

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"strconv"

	xterm "github.com/SurTeam/Water/internal/xterm"
)

const (
	maxGraphicsBufferBytes = 64 * 1024 * 1024
	maxImageBytes          = 32 * 1024 * 1024
	maxImagePixels         = 16 * 1024 * 1024
	maxImageDimension      = 8 * 1024
	maxStoredImageBytes    = 128 * 1024 * 1024
	maxImagePlacements     = 64 * 1024
	imageOverscanRows      = 64
)

const terminalImagePlaceholder = rune(0x10EEEE)

type TerminalImage struct {
	ID           uint64
	Row          int
	Column       int
	Width        int
	Height       int
	PixelWidth   int
	PixelHeight  int
	SourceX      int
	SourceY      int
	SourceWidth  int
	SourceHeight int
	RGBA         []byte
	ClipRow      int
	ClipColumn   int
	ClipWidth    int
	ClipHeight   int
}

type decodedImage struct {
	width  int
	height int
	rgba   []byte
}

type imagePlacement struct {
	marker       *xterm.Marker
	buffer       *xterm.Buffer
	column       int
	width        int
	height       int
	placementID  uint32
	sourceX      int
	sourceY      int
	sourceWidth  int
	sourceHeight int
}

func (p *imagePlacement) dispose() {
	if p != nil && p.marker != nil && !p.marker.IsDisposed {
		p.marker.Dispose()
	}
}

func (p *imagePlacement) row() int { return p.marker.Line - p.height + 1 }

type imageRecord struct {
	renderID           uint64
	width              int
	height             int
	rgba               []byte
	placements         []*imagePlacement
	unicodePlaceholder bool
	placeholderSize    [2]int
}

func (r *imageRecord) dispose() {
	for _, placement := range r.placements {
		placement.dispose()
	}
}

type pendingKitty struct {
	imageID uint32
	encoded []byte
	params  []byte
	action  byte
}

type graphicsState struct {
	parser  graphicsParser
	images  map[uint32]*imageRecord
	pending *pendingKitty

	nextProtocolID uint32
	nextRenderID   uint64
	storedBytes    int

	cellWidth  int
	cellHeight int
}

func newGraphicsState() *graphicsState {
	return &graphicsState{
		images:     make(map[uint32]*imageRecord),
		cellWidth:  8,
		cellHeight: 16,
	}
}

func (g *graphicsState) close() {
	for _, record := range g.images {
		record.dispose()
	}
	g.images = make(map[uint32]*imageRecord)
	g.pending = nil
	g.storedBytes = 0
}

func (g *graphicsState) setCellSize(width, height int) {
	if width > 0 {
		g.cellWidth = width
	}
	if height > 0 {
		g.cellHeight = height
	}
}

type graphicsKind uint8

const (
	graphicsKitty graphicsKind = iota + 1
	graphicsIterm
	graphicsSixel
)

type graphicsEvent struct {
	endOffset int
	kind      graphicsKind
	payload   []byte
}

type graphicsParser struct {
	buffer []byte
}

func (p *graphicsParser) feed(input []byte) []graphicsEvent {
	if len(input) == 0 {
		return nil
	}
	// Ordinary terminal text is overwhelmingly ASCII/UTF-8 without graphics
	// control introducers. When no parser carry exists, avoid the full graphics
	// scanner unless the chunk contains ESC or any high byte that could encode
	// a C1 control. This keeps the graphics feature effectively free for plain
	// terminal output while preserving Unicode/C1 correctness.
	if len(p.buffer) == 0 && !hasPotentialGraphicsByte(input) {
		return nil
	}
	if len(p.buffer) == 0 {
		if _, _, found := findGraphicsStart(input); !found {
			// Styled text does not need to be copied into a graphics buffer.
			// Preserve only a split introducer/UTF-8 suffix for the next read.
			if retain := graphicsCarryLen(input); retain > 0 {
				p.buffer = append(p.buffer, input[len(input)-retain:]...)
			}
			return nil
		}
	}

	previousLen := len(p.buffer)
	p.buffer = append(p.buffer, input...)
	consumed := 0
	var events []graphicsEvent
	for {
		start, kind, ok := findGraphicsStart(p.buffer)
		if !ok {
			retain := graphicsCarryLen(p.buffer)
			if retain == 0 {
				p.buffer = p.buffer[:0]
			} else if splitAt := len(p.buffer) - retain; splitAt > 0 {
				p.buffer = append(p.buffer[:0], p.buffer[splitAt:]...)
			}
			break
		}
		if start > 0 {
			p.buffer = append(p.buffer[:0], p.buffer[start:]...)
			consumed += start
		}
		headerLen := 0
		switch kind {
		case graphicsKitty:
			if len(p.buffer) >= 2 && p.buffer[0] == 0x9f && p.buffer[1] == 'G' {
				headerLen = 2
			} else {
				headerLen = 3
			}
		case graphicsIterm:
			if len(p.buffer) > 0 && p.buffer[0] == 0x9d {
				headerLen = 1
			} else {
				headerLen = 2
			}
		case graphicsSixel:
			if len(p.buffer) > 0 && p.buffer[0] == 0x90 {
				headerLen = 1
			} else {
				headerLen = 2
			}
		}
		if len(p.buffer) < headerLen {
			break
		}
		terminator, terminatorLen, ok := findGraphicsTerminator(p.buffer[headerLen:], kind == graphicsIterm)
		if !ok {
			if len(p.buffer) > maxGraphicsBufferBytes {
				p.buffer = p.buffer[:0]
			}
			break
		}
		payloadEnd := headerLen + terminator
		payload := append([]byte(nil), p.buffer[headerLen:payloadEnd]...)
		eventLen := payloadEnd + terminatorLen
		p.buffer = append(p.buffer[:0], p.buffer[eventLen:]...)
		consumed += eventLen
		endOffset := consumed - previousLen
		if endOffset < 0 {
			endOffset = 0
		}
		if endOffset > len(input) {
			endOffset = len(input)
		}
		events = append(events, graphicsEvent{
			endOffset: endOffset,
			kind:      kind,
			payload:   payload,
		})
	}
	return events
}

func hasPotentialGraphicsByte(data []byte) bool {
	for _, b := range data {
		if b == 0x1b || b >= 0x80 {
			return true
		}
	}
	return false
}

// graphicsCarryLen retains only bytes that can change how the next chunk is
// interpreted: a split ESC graphics introducer, a split raw C1 Kitty APC
// introducer, or an incomplete UTF-8 sequence whose continuation bytes may
// otherwise look like raw C1 controls. Plain text therefore leaves no carry.
func graphicsCarryLen(data []byte) int {
	n := len(data)
	if n == 0 {
		return 0
	}
	retain := 0
	if data[n-1] == 0x1b {
		retain = 1
	}
	if n >= 2 && data[n-2] == 0x1b && data[n-1] == '_' {
		retain = 2
	}
	if data[n-1] == 0x9f && !isUTF8Continuation(data, n-1) && retain < 1 {
		retain = 1
	}

	limit := 4
	if n < limit {
		limit = n
	}
	for back := 1; back <= limit; back++ {
		index := n - back
		b := data[index]
		if b < 0x80 {
			break
		}
		if b&0xc0 == 0x80 {
			continue
		}
		required := 0
		switch {
		case b >= 0xc2 && b <= 0xdf:
			required = 2
		case b >= 0xe0 && b <= 0xef:
			required = 3
		case b >= 0xf0 && b <= 0xf4:
			required = 4
		}
		if required > back && back > retain {
			retain = back
		}
		break
	}
	return retain
}

func findGraphicsStart(data []byte) (int, graphicsKind, bool) {
	for i := 0; i < len(data); i++ {
		if data[i] != 0x1b {
			// Only a graphics C1 introducer needs UTF-8 boundary validation.
			// ASCII/style text must not call the decoder for every byte.
			if (data[i] == 0x9f || data[i] == 0x9d || data[i] == 0x90) && !isUTF8Continuation(data, i) {
				if data[i] == 0x9f && i+1 < len(data) && data[i+1] == 'G' {
					return i, graphicsKitty, true
				}
				if data[i] == 0x9d {
					return i, graphicsIterm, true
				}
				if data[i] == 0x90 {
					return i, graphicsSixel, true
				}
			}
			continue
		}
		if i+2 < len(data) && data[i+1] == '_' && data[i+2] == 'G' {
			return i, graphicsKitty, true
		}
		if i+1 < len(data) && data[i+1] == ']' {
			return i, graphicsIterm, true
		}
		if i+1 < len(data) && data[i+1] == 'P' {
			return i, graphicsSixel, true
		}
	}
	return 0, 0, false
}

func isUTF8Continuation(data []byte, index int) bool {
	if index < 0 || index >= len(data) || data[index] < 0x80 || data[index] > 0xbf {
		return false
	}
	lead := index
	for lead > 0 && data[lead]&0xc0 == 0x80 {
		lead--
	}
	required := 0
	switch {
	case data[lead] >= 0xc2 && data[lead] <= 0xdf:
		required = 1
	case data[lead] >= 0xe0 && data[lead] <= 0xef:
		required = 2
	case data[lead] >= 0xf0 && data[lead] <= 0xf4:
		required = 3
	default:
		return false
	}
	delta := index - lead
	return delta >= 1 && delta <= required
}

func findGraphicsTerminator(data []byte, allowBEL bool) (int, int, bool) {
	for i := 0; i < len(data); i++ {
		if allowBEL && data[i] == 0x07 {
			return i, 1, true
		}
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i, 2, true
		}
		if data[i] == 0x9c {
			return i, 1, true
		}
	}
	return 0, 0, false
}

func (g *graphicsState) handle(term *xterm.Terminal, event graphicsEvent) (int, []byte) {
	switch event.kind {
	case graphicsKitty:
		return g.handleKitty(term, event.payload)
	case graphicsIterm:
		return g.handleIterm(term, event.payload), nil
	case graphicsSixel:
		return g.handleSixel(term, event.payload), nil
	default:
		return 0, nil
	}
}

func (g *graphicsState) handleKitty(term *xterm.Terminal, payload []byte) (int, []byte) {
	params, _ := splitBytes(payload, ';')
	action := firstParamByte(params, 'a', 't')
	id, explicit := paramUint(params, 'i')
	quiet := intParam(params, 'q', 0)
	if g.pending != nil && !explicit {
		id, explicit = g.pending.imageID, true
		quiet = intParam(g.pending.params, 'q', 0)
	}
	previousRender := g.nextRenderID
	rows, response := g.handleKittyCommand(term, payload)
	if action == 'q' || action == 'd' || !explicit || id == 0 || quiet == 2 || g.pending != nil {
		return rows, response
	}
	message := "OK"
	if action == 'p' && g.images[id] == nil {
		message = "ENOENT:image not found"
	} else if (action == 't' || action == 'T') && g.nextRenderID == previousRender {
		message = "EINVAL:invalid image data or unsupported medium"
	}
	if message == "OK" && quiet == 1 {
		return rows, nil
	}
	return rows, []byte("\x1b_Gi=" + strconv.FormatUint(uint64(id), 10) + ";" + message + "\x1b\\")
}

func (g *graphicsState) handleKittyCommand(term *xterm.Terminal, payload []byte) (int, []byte) {
	params, encoded := splitBytes(payload, ';')
	action := firstParamByte(params, 'a', 't')
	if action == 'q' {
		id, ok := paramUint(params, 'i')
		if !ok || id == 0 {
			return 0, nil
		}
		message := "OK"
		if transport := firstParamByte(params, 't', 0); transport != 0 && transport != 'd' {
			message = "EINVAL:unsupported transmission medium"
		}
		return 0, []byte("\x1b_Gi=" + strconv.FormatUint(uint64(id), 10) + ";" + message + "\x1b\\")
	}
	if action == 'd' {
		g.deleteKitty(term, params)
		return 0, nil
	}
	if action == 'p' {
		id, ok := paramUint(params, 'i')
		if !ok || id == 0 {
			return 0, nil
		}
		record := g.images[id]
		if record == nil {
			return 0, nil
		}
		if paramEquals(params, 'U', "1") {
			record.unicodePlaceholder = true
			record.placeholderSize = placeholderSize(params)
			return 0, nil
		}
		placement := g.place(term, id, params)
		if placement == nil {
			return 0, nil
		}
		if paramEquals(params, 'C', "1") {
			return 0, nil
		}
		advanceKittyCursor(term, placement)
		return 0, nil
	}
	if action != 't' && action != 'T' {
		return 0, nil
	}

	explicitID, hasID := paramUint(params, 'i')
	if hasID && explicitID == 0 {
		hasID = false
	}
	if hasID && g.pending != nil && g.pending.imageID != explicitID {
		g.pending = nil
	}
	imageID := explicitID
	if !hasID {
		if g.pending != nil {
			imageID = g.pending.imageID
		} else {
			imageID = g.allocateProtocolID()
		}
	}
	if g.pending == nil || g.pending.imageID != imageID {
		g.pending = &pendingKitty{
			imageID: imageID,
			params:  append([]byte(nil), params...),
			action:  action,
		}
	}
	if len(g.pending.encoded)+len(encoded) > maxGraphicsBufferBytes {
		g.pending = nil
		return 0, nil
	}
	g.pending.encoded = append(g.pending.encoded, encoded...)
	if value, ok := paramUint(params, 'm'); ok && value == 1 {
		return 0, nil
	}

	pending := g.pending
	g.pending = nil
	raw, err := decodeGraphicsBase64(pending.encoded)
	if err != nil {
		return 0, nil
	}
	if paramEquals(pending.params, 'o', "z") {
		raw = decompressBounded(raw)
		if raw == nil {
			return 0, nil
		}
	} else if value := paramValue(pending.params, 'o'); len(value) > 0 {
		return 0, nil
	}
	decoded := decodeKittyImage(raw, pending.params)
	if decoded == nil {
		return 0, nil
	}

	unicodePlaceholder := pending.action == 'T' && paramEquals(pending.params, 'U', "1")
	var placement *imagePlacement
	if pending.action == 'T' && !unicodePlaceholder {
		placement = g.newPlacement(term, decoded.width, decoded.height, pending.params)
	}
	g.store(imageID, decoded, placement, unicodePlaceholder, placeholderSize(pending.params))
	if placement != nil && !paramEquals(pending.params, 'C', "1") {
		advanceKittyCursor(term, placement)
	}
	return 0, nil
}

func advanceKittyCursor(term *xterm.Terminal, placement *imagePlacement) {
	// Line feed preserves the starting column; Kitty then moves to the right
	// edge as well as below the image. C=1 callers manage their own cursor.
	advance := bytes.Repeat([]byte{'\n'}, placement.height)
	advance = append(advance, []byte("\x1b["+strconv.Itoa(placement.width)+"C")...)
	_, _ = term.Write(advance)
}

func decompressBounded(raw []byte) []byte {
	reader, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	defer reader.Close()
	out, err := io.ReadAll(io.LimitReader(reader, maxImageBytes+1))
	if err != nil || len(out) > maxImageBytes {
		return nil
	}
	return out
}

func decodeGraphicsBase64(encoded []byte) ([]byte, error) {
	// Kitty's Go tools omit padding on the final chunk.
	if !bytes.ContainsRune(encoded, '=') {
		return base64.RawStdEncoding.DecodeString(string(encoded))
	}
	return base64.StdEncoding.DecodeString(string(encoded))
}

func decodeKittyImage(raw, params []byte) *decodedImage {
	medium := firstParamByte(params, 't', 'd')
	if medium != 'd' && medium != 'f' {
		return nil
	}
	if paramEquals(params, 't', "f") {
		if len(raw) == 0 || len(raw) > 4096 {
			return nil
		}
		info, err := os.Stat(string(raw))
		if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxImageBytes {
			return nil
		}
		data, err := os.ReadFile(string(raw))
		if err != nil {
			return nil
		}
		raw = data
	}
	format, _ := paramUint(params, 'f')
	switch format {
	case 24:
		width, okw := paramUint(params, 's')
		height, okh := paramUint(params, 'v')
		if !okw || !okh {
			return nil
		}
		return decodeRawImage(raw, int(width), int(height), 3)
	case 32:
		width, okw := paramUint(params, 's')
		height, okh := paramUint(params, 'v')
		if !okw || !okh {
			return nil
		}
		return decodeRawImage(raw, int(width), int(height), 4)
	default:
		return decodeEncodedImage(raw)
	}
}

func decodeEncodedImage(raw []byte) *decodedImage {
	if len(raw) == 0 || len(raw) > maxImageBytes {
		return nil
	}
	// image.Decode allocates the bitmap from the header before we can look at
	// it. A few kilobytes of PNG/JPEG/GIF can ask for a multi-gigabyte raster.
	config, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || !validImageSize(config.Width, config.Height, config.Width*config.Height*4) {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if !validImageSize(width, height, width*height*4) {
		return nil
	}
	rgba := image.NewNRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			pixel := color.NRGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.NRGBA)
			off := y*rgba.Stride + x*4
			rgba.Pix[off+0] = pixel.R
			rgba.Pix[off+1] = pixel.G
			rgba.Pix[off+2] = pixel.B
			rgba.Pix[off+3] = pixel.A
		}
	}
	return &decodedImage{width: width, height: height, rgba: rgba.Pix}
}

func decodeRawImage(raw []byte, width, height, channels int) *decodedImage {
	if width <= 0 || height <= 0 || channels < 3 || channels > 4 {
		return nil
	}
	pixels := width * height
	expected := pixels * channels
	if !validImageSize(width, height, expected) || expected > len(raw) {
		return nil
	}
	rgba := make([]byte, 0, pixels*4)
	for i := 0; i < expected; i += channels {
		rgba = append(rgba, raw[i], raw[i+1], raw[i+2])
		if channels == 4 {
			rgba = append(rgba, raw[i+3])
		} else {
			rgba = append(rgba, 255)
		}
	}
	return &decodedImage{width: width, height: height, rgba: rgba}
}

func validImageSize(width, height, byteLen int) bool {
	if width <= 0 || height <= 0 || width > maxImageDimension || height > maxImageDimension || byteLen <= 0 || byteLen > maxImageBytes {
		return false
	}
	return int64(width)*int64(height) <= maxImagePixels
}

func (g *graphicsState) handleIterm(term *xterm.Terminal, payload []byte) int {
	const prefix = "1337;File="
	if !bytes.HasPrefix(payload, []byte(prefix)) {
		return 0
	}
	rest := payload[len(prefix):]
	colon := bytes.IndexByte(rest, ':')
	if colon < 0 {
		return 0
	}
	options := parseSemicolonOptions(rest[:colon])
	if string(options["inline"]) != "1" {
		return 0
	}
	raw, err := base64.StdEncoding.DecodeString(string(rest[colon+1:]))
	if err != nil {
		return 0
	}
	decoded := decodeEncodedImage(raw)
	if decoded == nil {
		return 0
	}
	width := parseDimension(options["width"], g.cellWidth)
	height := parseDimension(options["height"], g.cellHeight)
	placement := g.newDimensionPlacement(term, decoded.width, decoded.height, width, height)
	if placement == nil {
		return 0
	}
	if string(options["preserveAspectRatio"]) == "0" {
		if width > 0 {
			placement.width = width
		}
		if height > 0 {
			placement.height = height
		}
	}
	id := g.allocateProtocolID()
	g.store(id, decoded, placement, false, [2]int{})
	return placement.height
}

func parseSemicolonOptions(raw []byte) map[string][]byte {
	result := make(map[string][]byte)
	for _, part := range bytes.Split(raw, []byte(";")) {
		if eq := bytes.IndexByte(part, '='); eq >= 0 {
			result[string(part[:eq])] = part[eq+1:]
		}
	}
	return result
}

func parseDimension(raw []byte, cellPixels int) int {
	if len(raw) == 0 || bytes.HasSuffix(raw, []byte("%")) {
		return 0
	}
	if bytes.HasSuffix(raw, []byte("px")) {
		value, err := strconv.Atoi(string(raw[:len(raw)-2]))
		if err != nil || value <= 0 {
			return 0
		}
		return ceilDiv(value, maxInt(cellPixels, 1))
	}
	value, err := strconv.Atoi(string(raw))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

func (g *graphicsState) handleSixel(term *xterm.Terminal, payload []byte) int {
	decoded := decodeSixel(payload)
	if decoded == nil {
		return 0
	}
	placement := g.newDimensionPlacement(term, decoded.width, decoded.height, 0, 0)
	if placement == nil {
		return 0
	}
	id := g.allocateProtocolID()
	g.store(id, decoded, placement, false, [2]int{})
	return 0
}

func decodeSixel(payload []byte) *decodedImage {
	q := bytes.IndexByte(payload, 'q')
	if q < 0 {
		return nil
	}
	for _, b := range payload[:q] {
		if b < 0x30 || b > 0x3f {
			return nil
		}
	}
	data := payload[q+1:]
	canvas := newSixelCanvas()
	var colors [256][4]byte
	for i := range colors {
		colors[i] = [4]byte{255, 255, 255, 255}
	}
	colorIndex := 0
	x, y := 0, 0
	for i := 0; i < len(data); {
		switch data[i] {
		case '"':
			values, used := sixelNumbers(data[i+1:])
			if len(values) >= 4 && values[2] > 0 && values[3] > 0 {
				canvas.ensure(values[2], values[3])
			}
			i += used + 1
		case '#':
			number, used := sixelNumber(data[i+1:])
			if number >= 0 {
				if number > 255 {
					number = 255
				}
				colorIndex = number
			}
			pos := i + 1 + used
			if pos < len(data) && data[pos] == ';' {
				values, consumed := sixelNumbers(data[pos+1:])
				if len(values) >= 4 && values[0] == 2 {
					colors[colorIndex] = [4]byte{
						byte(minInt(values[1], 100) * 255 / 100),
						byte(minInt(values[2], 100) * 255 / 100),
						byte(minInt(values[3], 100) * 255 / 100),
						255,
					}
				}
				used += consumed + 1
			}
			i += used + 1
		case '!':
			repeat, used := sixelNumber(data[i+1:])
			if repeat < 1 {
				repeat = 1
			}
			if repeat > 4096 {
				repeat = 4096
			}
			pos := i + used + 1
			if pos >= len(data) {
				return canvas.finish()
			}
			ch := data[pos]
			if ch >= '?' && ch <= '~' {
				for n := 0; n < repeat; n++ {
					drawSixel(canvas, &x, y, ch, colors[colorIndex])
				}
			}
			i += used + 2
		case '$':
			x = 0
			i++
		case '-':
			x = 0
			y += 6
			i++
		default:
			ch := data[i]
			if ch >= '?' && ch <= '~' {
				drawSixel(canvas, &x, y, ch, colors[colorIndex])
			}
			i++
		}
	}
	return canvas.finish()
}

type sixelCanvas struct {
	width, height int
	pixels        []byte
	maxX, maxY    int
}

func newSixelCanvas() *sixelCanvas { return &sixelCanvas{} }

func (c *sixelCanvas) ensure(requiredWidth, requiredHeight int) {
	requiredWidth = minInt(maxInt(requiredWidth, 1), maxImageDimension)
	requiredHeight = minInt(maxInt(requiredHeight, 1), maxImageDimension)
	if requiredWidth <= c.width && requiredHeight <= c.height {
		return
	}
	width := maxInt(requiredWidth, maxInt(c.width*2, 1))
	height := maxInt(requiredHeight, maxInt(c.height*2, 1))
	width = minInt(width, maxImageDimension)
	height = minInt(height, maxImageDimension)
	if int64(width)*int64(height) > maxImagePixels || width*height*4 > maxImageBytes {
		return
	}
	pixels := make([]byte, width*height*4)
	for row := 0; row < minInt(c.height, height); row++ {
		copy(
			pixels[row*width*4:row*width*4+minInt(c.width, width)*4],
			c.pixels[row*c.width*4:row*c.width*4+minInt(c.width, width)*4],
		)
	}
	c.width, c.height, c.pixels = width, height, pixels
}

func (c *sixelCanvas) set(x, y int, color [4]byte) {
	if x < 0 || y < 0 || x >= maxImageDimension || y >= maxImageDimension {
		return
	}
	c.ensure(x+1, y+1)
	if x >= c.width || y >= c.height {
		return
	}
	off := (y*c.width + x) * 4
	copy(c.pixels[off:off+4], color[:])
	c.maxX = maxInt(c.maxX, x+1)
	c.maxY = maxInt(c.maxY, y+1)
}

func (c *sixelCanvas) finish() *decodedImage {
	if c.maxX == 0 || c.maxY == 0 {
		return nil
	}
	rgba := make([]byte, 0, c.maxX*c.maxY*4)
	for row := 0; row < c.maxY; row++ {
		start := row * c.width * 4
		rgba = append(rgba, c.pixels[start:start+c.maxX*4]...)
	}
	if !validImageSize(c.maxX, c.maxY, len(rgba)) {
		return nil
	}
	return &decodedImage{width: c.maxX, height: c.maxY, rgba: rgba}
}

func drawSixel(canvas *sixelCanvas, x *int, y int, ch byte, color [4]byte) {
	bits := ch - '?'
	for bit := 0; bit < 6; bit++ {
		if bits&(1<<bit) != 0 {
			canvas.set(*x, y+bit, color)
		}
	}
	*x = *x + 1
}

func sixelNumber(data []byte) (int, int) {
	value, used := 0, 0
	for used < len(data) && data[used] >= '0' && data[used] <= '9' {
		value = value*10 + int(data[used]-'0')
		used++
	}
	if used == 0 {
		return -1, 0
	}
	return value, used
}

func sixelNumbers(data []byte) ([]int, int) {
	var values []int
	used := 0
	for used < len(data) {
		value, length := sixelNumber(data[used:])
		if value < 0 {
			break
		}
		values = append(values, value)
		used += length
		if used >= len(data) || data[used] != ';' {
			break
		}
		used++
	}
	return values, used
}

func (g *graphicsState) store(id uint32, decoded *decodedImage, placement *imagePlacement, unicodePlaceholder bool, placeholderSize [2]int) {
	if decoded == nil || len(decoded.rgba) == 0 || len(decoded.rgba) > maxImageBytes {
		if placement != nil {
			placement.dispose()
		}
		return
	}
	previousBytes := 0
	if previous := g.images[id]; previous != nil {
		previousBytes = len(previous.rgba)
	}
	withoutPrevious := g.storedBytes - previousBytes
	if len(decoded.rgba) > maxStoredImageBytes-withoutPrevious {
		if placement != nil {
			placement.dispose()
		}
		return
	}
	if previous := g.images[id]; previous != nil {
		previous.dispose()
		g.storedBytes -= len(previous.rgba)
	}
	g.nextRenderID++
	if g.nextRenderID == 0 {
		g.nextRenderID = 1
	}
	record := &imageRecord{
		renderID:           g.nextRenderID,
		width:              decoded.width,
		height:             decoded.height,
		rgba:               decoded.rgba,
		unicodePlaceholder: unicodePlaceholder,
		placeholderSize:    placeholderSize,
	}
	if placement != nil {
		record.placements = []*imagePlacement{placement}
	}
	g.images[id] = record
	g.storedBytes += len(decoded.rgba)
}

func (g *graphicsState) place(term *xterm.Terminal, id uint32, params []byte) *imagePlacement {
	record := g.images[id]
	if record == nil {
		return nil
	}
	placement := g.newPlacement(term, record.width, record.height, params)
	if placement == nil {
		return nil
	}
	if placement.placementID != 0 {
		for i, existing := range record.placements {
			if existing.placementID == placement.placementID {
				existing.dispose()
				record.placements[i] = placement
				return placement
			}
		}
	}
	if len(record.placements) >= maxImagePlacements {
		placement.dispose()
		return nil
	}
	record.placements = append(record.placements, placement)
	return placement
}

func (g *graphicsState) newPlacement(term *xterm.Terminal, imageWidth, imageHeight int, params []byte) *imagePlacement {
	sourceX := intParam(params, 'x', 0)
	sourceY := intParam(params, 'y', 0)
	sourceX = minInt(maxInt(sourceX, 0), maxInt(imageWidth-1, 0))
	sourceY = minInt(maxInt(sourceY, 0), maxInt(imageHeight-1, 0))
	sourceWidth := intParam(params, 'w', imageWidth-sourceX)
	sourceHeight := intParam(params, 'h', imageHeight-sourceY)
	sourceWidth = minInt(maxInt(sourceWidth, 1), imageWidth-sourceX)
	sourceHeight = minInt(maxInt(sourceHeight, 1), imageHeight-sourceY)
	width := intParam(params, 'c', 0)
	height := intParam(params, 'r', 0)
	placement := g.newDimensionPlacement(term, sourceWidth, sourceHeight, width, height)
	if placement == nil {
		return nil
	}
	placement.sourceX = sourceX
	placement.sourceY = sourceY
	placement.sourceWidth = sourceWidth
	placement.sourceHeight = sourceHeight
	if id, ok := paramUint(params, 'p'); ok {
		placement.placementID = id
	}
	return placement
}

func (g *graphicsState) newDimensionPlacement(term *xterm.Terminal, pixelWidth, pixelHeight, requestedWidth, requestedHeight int) *imagePlacement {
	if term == nil || pixelWidth <= 0 || pixelHeight <= 0 {
		return nil
	}
	cellWidth := maxInt(g.cellWidth, 1)
	cellHeight := maxInt(g.cellHeight, 1)
	width, height := requestedWidth, requestedHeight
	switch {
	case width > 0 && height > 0:
	case width > 0:
		height = ceilDiv(pixelHeight*width*cellWidth, pixelWidth*cellHeight)
	case height > 0:
		width = ceilDiv(pixelWidth*height*cellHeight, pixelHeight*cellWidth)
	default:
		width = ceilDiv(pixelWidth, cellWidth)
		height = ceilDiv(pixelHeight, cellHeight)
	}
	width, height = minInt(maxInt(width, 1), maxImageDimension), minInt(maxInt(height, 1), maxImageDimension)
	return &imagePlacement{
		// Anchor the last covered row: trimming the top of a tall image must
		// retain the part still present in history.
		marker:       term.RegisterMarker(height - 1),
		buffer:       term.Buffer(),
		column:       term.CursorX(),
		width:        width,
		height:       height,
		sourceWidth:  pixelWidth,
		sourceHeight: pixelHeight,
	}
}

func (g *graphicsState) deleteKitty(term *xterm.Terminal, params []byte) {
	mode := firstParamByte(params, 'd', 'a')
	switch mode {
	case 'r', 'R':
		first, last := uint32(intParam(params, 'x', 0)), uint32(intParam(params, 'y', int(^uint32(0))))
		for id, record := range g.images {
			if id < first || id > last {
				continue
			}
			record.dispose()
			record.placements = nil
			record.unicodePlaceholder = false
			if mode == 'R' {
				g.storedBytes -= len(record.rgba)
				delete(g.images, id)
			}
		}
	case 'a', 'A':
		// Screen deletion must leave placements already in history intact.
		buf := term.Buffer()
		for id, record := range g.images {
			kept := record.placements[:0]
			for _, p := range record.placements {
				if p.buffer == buf && p.marker != nil && !p.marker.IsDisposed && p.row()+p.height > buf.YBase && p.row() < buf.YBase+term.Rows() {
					p.dispose()
				} else {
					kept = append(kept, p)
				}
			}
			record.placements = kept
			if mode == 'A' && len(kept) == 0 && !record.unicodePlaceholder {
				g.storedBytes -= len(record.rgba)
				delete(g.images, id)
			}
		}
	case 'i', 'I':
		id, ok := paramUint(params, 'i')
		if !ok {
			return
		}
		record := g.images[id]
		if record == nil {
			return
		}
		if placementID, ok := paramUint(params, 'p'); ok && placementID != 0 {
			filtered := record.placements[:0]
			for _, placement := range record.placements {
				if placement.placementID == placementID {
					placement.dispose()
				} else {
					filtered = append(filtered, placement)
				}
			}
			record.placements = filtered
			return
		}
		record.dispose()
		record.placements = nil
		record.unicodePlaceholder = false
		if mode == 'I' {
			g.storedBytes -= len(record.rgba)
			delete(g.images, id)
		}
	}
}

func (g *graphicsState) clear() {
	for _, record := range g.images {
		record.dispose()
	}
	clear(g.images)
	g.pending = nil
	g.storedBytes = 0
}

func (g *graphicsState) allocateProtocolID() uint32 {
	for {
		g.nextProtocolID++
		if g.nextProtocolID == 0 {
			g.nextProtocolID = 1
		}
		if g.images[g.nextProtocolID] == nil && (g.pending == nil || g.pending.imageID != g.nextProtocolID) {
			return g.nextProtocolID
		}
	}
}

func (g *graphicsState) eraseVisible(term *xterm.Terminal) {
	if g == nil || term == nil {
		return
	}
	buf := term.Buffer()
	start := buf.YDisp
	end := start + term.Rows()
	g.retainPlacements(func(p *imagePlacement) bool {
		if p == nil || p.marker == nil || p.marker.IsDisposed || p.marker.Line < 0 {
			return false
		}
		if p.buffer != buf {
			return true
		}
		pEnd := p.row() + maxInt(p.height, 1)
		return pEnd <= start || p.row() >= end
	})
}

func (g *graphicsState) eraseScrollback(term *xterm.Terminal) {
	if g == nil || term == nil {
		return
	}
	start := term.Buffer().YDisp
	g.retainPlacements(func(p *imagePlacement) bool {
		return p != nil && p.marker != nil && !p.marker.IsDisposed && (p.buffer != term.Buffer() || p.row()+p.height > start)
	})
}

func (g *graphicsState) retainPlacements(keep func(*imagePlacement) bool) {
	for id, record := range g.images {
		if record == nil {
			delete(g.images, id)
			continue
		}
		if len(record.placements) > 0 {
			next := record.placements[:0]
			for _, placement := range record.placements {
				if keep(placement) {
					next = append(next, placement)
				} else {
					placement.dispose()
				}
			}
			record.placements = next
		}
		if len(record.placements) == 0 && !record.unicodePlaceholder {
			g.storedBytes -= len(record.rgba)
			if g.storedBytes < 0 {
				g.storedBytes = 0
			}
			delete(g.images, id)
		}
	}
}

func (g *graphicsState) snapshot(term *xterm.Terminal, rows []Row) []TerminalImage {
	if g == nil || term == nil || len(g.images) == 0 {
		return nil
	}
	buf := term.Buffer()
	var out []TerminalImage
	for protocolID, record := range g.images {
		if record == nil {
			continue
		}
		if len(record.placements) > 0 {
			kept := record.placements[:0]
			for _, p := range record.placements {
				if p != nil && p.marker != nil && !p.marker.IsDisposed {
					kept = append(kept, p)
				}
			}
			record.placements = kept
			if len(kept) == 0 && !record.unicodePlaceholder {
				g.storedBytes -= len(record.rgba)
				delete(g.images, protocolID)
				continue
			}
		}
		if record.unicodePlaceholder {
			out = append(out, g.placeholderSnapshot(protocolID, record, rows)...)
		}
		for _, placement := range record.placements {
			if placement == nil || placement.buffer != buf || placement.marker == nil || placement.marker.IsDisposed || placement.marker.Line < 0 {
				continue
			}
			row := placement.row() - buf.YDisp
			if row+placement.height < -imageOverscanRows || row > term.Rows()+imageOverscanRows {
				continue
			}
			out = append(out, terminalImageFromRecord(record, row, placement.column, placement.width, placement.height, placement))
		}
	}
	return out
}

func terminalImageFromRecord(record *imageRecord, row, column, width, height int, placement *imagePlacement) TerminalImage {
	sourceX, sourceY := 0, 0
	sourceWidth, sourceHeight := record.width, record.height
	if placement != nil {
		sourceX, sourceY = placement.sourceX, placement.sourceY
		sourceWidth, sourceHeight = placement.sourceWidth, placement.sourceHeight
	}
	return TerminalImage{
		ID:           record.renderID,
		Row:          row,
		Column:       column,
		Width:        maxInt(width, 1),
		Height:       maxInt(height, 1),
		PixelWidth:   record.width,
		PixelHeight:  record.height,
		SourceX:      sourceX,
		SourceY:      sourceY,
		SourceWidth:  maxInt(sourceWidth, 1),
		SourceHeight: maxInt(sourceHeight, 1),
		RGBA:         record.rgba,
	}
}

func placeholderImageID(color Color) uint32 {
	switch color.Mode {
	case ColorPalette, ColorRGB:
		return color.Value
	default:
		return 0
	}
}

func splitBytes(data []byte, separator byte) ([]byte, []byte) {
	if i := bytes.IndexByte(data, separator); i >= 0 {
		return data[:i], data[i+1:]
	}
	return data, nil
}

func paramValue(params []byte, key byte) []byte {
	for _, part := range bytes.Split(params, []byte(",")) {
		if len(part) >= 2 && part[0] == key && part[1] == '=' {
			return part[2:]
		}
	}
	return nil
}

func paramEquals(params []byte, key byte, want string) bool {
	return string(paramValue(params, key)) == want
}

func paramUint(params []byte, key byte) (uint32, bool) {
	raw := paramValue(params, key)
	if len(raw) == 0 {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw), 10, 32)
	return uint32(value), err == nil
}

func intParam(params []byte, key byte, fallback int) int {
	value, ok := paramUint(params, key)
	if !ok {
		return fallback
	}
	return int(value)
}

func firstParamByte(params []byte, key, fallback byte) byte {
	raw := paramValue(params, key)
	if len(raw) == 0 {
		return fallback
	}
	return raw[0]
}

func placeholderSize(params []byte) [2]int {
	return [2]int{intParam(params, 'c', 0), intParam(params, 'r', 0)}
}

func ceilDiv(numerator, denominator int) int {
	if denominator <= 0 {
		return 0
	}
	return (numerator + denominator - 1) / denominator
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
