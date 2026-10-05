package goui

import (
	"fmt"
	"image"
	"math"
	"sort"
	"time"

	"github.com/SurTeam/Water/internal/govt"

	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"github.com/hajimehoshi/ebiten/v2/vector"
)

type nativeCellMetrics struct {
	width, height int
	baseline      float64
}

func measureNativeCell(faces []text.Face, requestedHeight int) nativeCellMetrics {
	var ascent, descent float64
	for _, face := range faces {
		metrics := face.Metrics()
		ascent, descent = max(ascent, metrics.HAscent), max(descent, metrics.HDescent)
	}
	width, _ := text.Measure("M", faces[0], 0)
	// One physical pixel of clearance also preserves antialiased glyph edges.
	height := max(requestedHeight, int(math.Ceil(ascent+descent))+2)
	return nativeCellMetrics{max(1, int(width+.5)), height, math.Floor((float64(height)-ascent-descent)/2) + ascent}
}

func (w *EbitengineWindow) terminalFaces(c *WorkspaceClient) []text.Face {
	cfg := c.currentConfig()
	size := float64(w.dp(float64(cfg.Terminal.FontSize)))
	return []text.Face{
		w.fonts.face(cfg.Terminal.FontFamily, size, true, false, false, cfg.Terminal.Ligatures),
		w.fonts.cachedFace(cfg.Terminal.FontFamily, size, true, true, false, cfg.Terminal.Ligatures),
		w.fonts.cachedFace(cfg.Terminal.FontFamily, size, true, false, true, cfg.Terminal.Ligatures),
		w.fonts.cachedFace(cfg.Terminal.FontFamily, size, true, true, true, cfg.Terminal.Ligatures),
	}
}

type nativeRowTexture struct {
	hash  uint64
	image *ebiten.Image
}
type nativeTerminalTexture struct {
	rows    map[int]nativeRowTexture
	images  map[uint64]*ebiten.Image
	style   string
	surface *ebiten.Image
}

// Match content before recycling any texture: scrolling moves unchanged rows
// to different screen indices. Each old texture is consumed at most once.
func matchNativeRows(rows []govt.Row, old map[int]nativeRowTexture) ([]int, []int) {
	byHash := make(map[uint64][]int, len(old))
	for y, row := range old {
		byHash[row.hash] = append(byHash[row.hash], y)
	}
	matches := make([]int, len(rows))
	used := make(map[int]bool, len(old))
	for y, row := range rows {
		matches[y] = -1
		if indices := byHash[row.Hash]; len(indices) > 0 {
			index := indices[len(indices)-1]
			matches[y], used[index] = index, true
			byHash[row.Hash] = indices[:len(indices)-1]
		}
	}
	var unused []int
	for y := range old {
		if !used[y] {
			unused = append(unused, y)
		}
	}
	sort.Ints(unused)
	return matches, unused
}

func recycleNativeRows(cache *nativeTerminalTexture, v *TerminalView, rows []govt.Row) {
	matches, unused := matchNativeRows(rows, cache.rows)
	next := make(map[int]nativeRowTexture, len(rows))
	prepared := make(map[int]preparedRow, len(rows))
	for y, index := range matches {
		if index >= 0 {
			next[y] = cache.rows[index]
			if row, ok := v.cache[index]; ok {
				prepared[y] = row
			}
		} else if len(unused) > 0 {
			index, unused = unused[len(unused)-1], unused[:len(unused)-1]
			// The image can be overwritten, but its old content cannot be used.
			next[y] = cache.rows[index]
			if row, ok := v.cache[index]; ok {
				row.hash = ^rows[y].Hash
				prepared[y] = row
			}
		}
	}
	cache.rows, v.cache = next, prepared
}

func (w *EbitengineWindow) resizeTerminal(c *WorkspaceClient, term *terminalClient, r image.Rectangle, cw, lh int) {
	cols, rows := min(512, max(2, r.Dx()/cw)), min(256, max(1, r.Dy()/lh))
	term.mu.Lock()
	if term.emu == nil {
		term.mu.Unlock()
		return
	}
	cellChanged := cw != term.cellWidth || lh != term.cellHeight
	// Pixel metrics belong to this client's renderer, including before native
	// focus arrives. Only changing the shared PTY's grid requires focus.
	if cellChanged {
		term.emu.SetCellSize(cw, lh)
		term.cellWidth, term.cellHeight = cw, lh
	}
	if !c.nativeFocused {
		if cellChanged {
			term.snapshot = term.emu.FrameSnapshot()
		}
		term.mu.Unlock()
		return
	}
	if cols == term.cols && rows == term.rows && !cellChanged && term.resizeGeneration == c.focusGeneration {
		term.mu.Unlock()
		return
	}
	if cols != term.cols || rows != term.rows {
		term.emu.Resize(cols, rows)
	}
	term.cols, term.rows = cols, rows
	term.resizeGeneration = c.focusGeneration
	term.snapshot = term.emu.FrameSnapshot()
	term.mu.Unlock()
	_ = c.session.DispatchAsync(map[string]any{"type": "terminal.resize", "terminal_id": term.id, "columns": cols, "lines": rows, "cell_width": cw, "cell_height": lh})
}

func (c *WorkspaceClient) updateWindowFocus(focused bool) {
	if c.focusKnown && c.nativeFocused == focused {
		return
	}
	if err := c.session.Notify("session.focus", map[string]any{"focused": focused}); err != nil {
		return
	}
	c.focusKnown, c.nativeFocused = true, focused
	if focused {
		c.focusGeneration++
	}
}

func (w *EbitengineWindow) drawTerminal(c *WorkspaceClient, dst *ebiten.Image, term *terminalClient, r image.Rectangle, cw, lh int, focused bool) {
	r = r.Intersect(dst.Bounds())
	if r.Empty() {
		return
	}
	dst = dst.SubImage(r).(*ebiten.Image)
	term.mu.RLock()
	snap, selection := term.snapshot, term.selection
	term.mu.RUnlock()
	v := term.view
	cfg := c.currentConfig()
	faces := w.terminalFaces(c)
	requested := [4]bool{true}
	faceForStyle := func(index int) text.Face {
		if !requested[index] {
			faces[index] = w.fonts.face(cfg.Terminal.FontFamily, float64(w.dp(float64(cfg.Terminal.FontSize))), true, index&1 != 0, index&2 != 0, cfg.Terminal.Ligatures)
			requested[index] = true
		}
		return faces[index]
	}
	if !cfg.Features.Selection {
		selection = Selection{}
	}
	cell := measureNativeCell(faces, lh)
	v.mu.Lock()
	defer v.mu.Unlock()
	applyTerminalColors(v, snap)
	cache := w.textures[term.id]
	if cache == nil {
		cache = &nativeTerminalTexture{rows: map[int]nativeRowTexture{}, images: map[uint64]*ebiten.Image{}}
		w.textures[term.id] = cache
	}
	style := fmt.Sprintf("%s:%g:%d:%d:%v:%t:%t:%d", cfg.Terminal.FontFamily, cfg.Terminal.FontSize, cw, lh, v.Theme, cfg.Terminal.Hyperlinks, cfg.Terminal.Ligatures, w.fonts.generation.Load())
	rowCount := min(snap.Rows, len(snap.RowsData), (r.Dy()+lh-1)/lh)
	width, height := max(1, snap.Cols*cw), max(1, rowCount*lh)
	if style != cache.style || cache.surface == nil || cache.surface.Bounds().Size() != image.Pt(width, height) {
		if cache.surface != nil {
			cache.surface.Deallocate()
		}
		// A large, repeatedly updated render target must not expand the shared
		// glyph atlas to the next power-of-two size when its rows are sampled.
		cache.surface = ebiten.NewImageWithOptions(image.Rect(0, 0, width, height), &ebiten.NewImageOptions{Unmanaged: true})
		cache.rows = map[int]nativeRowTexture{}
		v.cache = map[int]preparedRow{}
		cache.style = style
	}
	endMatch := traceNativeWork("terminal.row_match")
	recycleNativeRows(cache, v, snap.RowsData[:rowCount])
	if endMatch != nil {
		endMatch()
	}
	endRaster := traceNativeWork("terminal.raster")
	preparedRows, rasterRows, textRuns := 0, 0, 0
	// All row slots share a render target. Finish its updates before reading
	// any slot into the window, avoiding one Metal pass switch per row.
	for y, row := range snap.RowsData[:rowCount] {
		prepared, ok := v.cache[y]
		if !ok || prepared.hash != row.Hash {
			preparedRows++
			prepared = v.prepareRowInto(row, prepared)
			v.cache[y] = prepared
		}
		rendered, ok := cache.rows[y]
		if !ok || rendered.hash != row.Hash {
			rasterRows++
			var target *ebiten.Image
			if ok {
				target = rendered.image
			} else {
				target = cache.surface.SubImage(image.Rect(0, y*lh, width, (y+1)*lh)).(*ebiten.Image)
			}
			top := target.Bounds().Min.Y
			target.Fill(v.Theme.Background)
			for _, bg := range prepared.backgrounds {
				nativeRect(target, image.Rect(bg.startColumn*cw, top, (bg.startColumn+bg.spanColumns)*cw, top+lh), bg.color)
			}
			for _, run := range prepared.text {
				textRuns++
				bounds := image.Rect(run.startColumn*cw, top, (run.startColumn+run.spanColumns)*cw, top+lh).Intersect(target.Bounds())
				if bounds.Empty() || run.text == "" {
					continue
				}
				fg := run.style.fg
				if run.style.dim {
					fg.A = uint8(uint16(fg.A) * 2 / 3)
				}
				styleIndex := 0
				if run.style.bold {
					styleIndex++
				}
				if run.style.italic {
					styleIndex += 2
				}
				face := faceForStyle(styleIndex)
				glyphBounds := bounds
				if run.drawColumns > run.spanColumns {
					glyphBounds.Max.X = min(target.Bounds().Max.X, bounds.Min.X+run.drawColumns*cw)
				}
				if rects, block := terminalDrawingRects(run.text, bounds.Dx(), lh); block {
					for _, rect := range rects {
						nativeRect(target, rect.Add(bounds.Min), fg)
					}
				} else {
					drawTerminalGlyph(target, run.text, face, glyphBounds, cell, fg)
				}
				if run.style.underline {
					underline := bounds
					if run.style.linkURI != "" {
						underline = glyphBounds
					}
					nativeRect(target, image.Rect(underline.Min.X, top+lh-2, underline.Max.X, top+lh-1), fg)
				}
				if run.style.strikethrough {
					nativeRect(target, image.Rect(bounds.Min.X, top+lh/2, bounds.Max.X, top+lh/2+1), fg)
				}
			}
			rendered = nativeRowTexture{row.Hash, target}
			cache.rows[y] = rendered
		}
	}
	if endRaster != nil {
		endRaster()
	}
	countNativeWork("count.visible_rows", rowCount)
	countNativeWork("count.prepared_rows", preparedRows)
	countNativeWork("count.raster_rows", rasterRows)
	countNativeWork("count.text_runs", textRuns)
	endComposite := traceNativeWork("terminal.composite")
	if endComposite != nil {
		defer endComposite()
	}
	nativeRect(dst, r, v.Theme.Background)
	for y := 0; y < rowCount; y++ {
		rendered := cache.rows[y]
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Translate(float64(r.Min.X), float64(r.Min.Y+y*lh))
		dst.DrawImage(rendered.image, op)
		if left, right, ok := selectionColumnsForSnapshot(snap, selection, y); ok {
			clr := v.Theme.Selection
			clr.A = 100
			nativeRect(dst, image.Rect(r.Min.X+left*cw, r.Min.Y+y*lh, r.Min.X+(right+1)*cw, r.Min.Y+(y+1)*lh), clr)
		}
	}
	visibleImages := map[uint64]bool{}
	for _, graphic := range snap.Images {
		visibleImages[graphic.ID] = true
		tex := cache.images[graphic.ID]
		if tex == nil && graphic.PixelWidth > 0 && graphic.PixelHeight > 0 && len(graphic.RGBA) == graphic.PixelWidth*graphic.PixelHeight*4 {
			tex = ebiten.NewImage(graphic.PixelWidth, graphic.PixelHeight)
			tex.WritePixels(graphic.RGBA)
			cache.images[graphic.ID] = tex
		}
		if tex == nil {
			continue
		}
		source := image.Rect(graphic.SourceX, graphic.SourceY, graphic.SourceX+graphic.SourceWidth, graphic.SourceY+graphic.SourceHeight).Intersect(tex.Bounds())
		if source.Empty() {
			source = tex.Bounds()
		}
		op := &ebiten.DrawImageOptions{}
		op.GeoM.Translate(float64(-source.Min.X), float64(-source.Min.Y))
		op.GeoM.Scale(float64(graphic.Width*cw)/float64(source.Dx()), float64(graphic.Height*lh)/float64(source.Dy()))
		op.GeoM.Translate(float64(r.Min.X+graphic.Column*cw), float64(r.Min.Y+graphic.Row*lh))
		imageDst := dst
		if graphic.ClipWidth > 0 && graphic.ClipHeight > 0 {
			clip := image.Rect(r.Min.X+graphic.ClipColumn*cw, r.Min.Y+graphic.ClipRow*lh, r.Min.X+(graphic.ClipColumn+graphic.ClipWidth)*cw, r.Min.Y+(graphic.ClipRow+graphic.ClipHeight)*lh).Intersect(dst.Bounds())
			if clip.Empty() {
				continue
			}
			imageDst = dst.SubImage(clip).(*ebiten.Image)
		}
		imageDst.DrawImage(tex.SubImage(source).(*ebiten.Image), op)
	}
	for id, tex := range cache.images {
		if !visibleImages[id] {
			tex.Deallocate()
			delete(cache.images, id)
		}
	}
	if !snap.CursorHide && snap.CursorX >= 0 && snap.CursorX < snap.Cols && snap.CursorY >= 0 && snap.CursorY < snap.Rows {
		x, y := r.Min.X+snap.CursorX*cw, r.Min.Y+snap.CursorY*lh
		glyph, columns, face := "", 1, faces[0]
		if snap.CursorY < len(snap.RowsData) && snap.CursorX < len(snap.RowsData[snap.CursorY].Cells) {
			cells, column := snap.RowsData[snap.CursorY].Cells, snap.CursorX
			if cells[column].Width == 0 && column > 0 && cells[column-1].Width == 2 {
				column--
				x -= cw
			}
			cursorCell := cells[column]
			glyph, columns = cursorCell.Text, v.glyphDrawColumns(cells, column)
			styleIndex := 0
			if cursorCell.Bold {
				styleIndex++
			}
			if cursorCell.Italic {
				styleIndex += 2
			}
			face = faceForStyle(styleIndex)
		}
		clr := v.Theme.InactiveCursor
		if focused {
			clr = v.Theme.Cursor
		}
		if !focused || !snap.CursorBlink || time.Now().UnixMilli()%1000 < 600 {
			cursorRect := image.Rect(x, y, x+columns*cw, y+lh)
			if !focused {
				vector.StrokeRect(dst, float32(x)+.5, float32(y)+.5, float32(columns*cw-1), float32(lh-1), 1, clr, false)
			} else if snap.CursorStyle == "underline" {
				nativeRect(dst, image.Rect(x, y+lh-w.dp(2), x+columns*cw, y+lh), clr)
			} else if snap.CursorStyle == "bar" {
				nativeRect(dst, image.Rect(x, y, x+w.dp(2), y+lh), clr)
			} else {
				nativeRect(dst, cursorRect, clr)
				if glyph != "" {
					if rects, drawing := terminalDrawingRects(glyph, columns*cw, lh); drawing {
						for _, rect := range rects {
							nativeRect(dst, rect.Add(image.Pt(x, y)).Intersect(cursorRect).Intersect(dst.Bounds()), v.Theme.CursorForeground)
						}
					} else {
						drawTerminalGlyph(dst, glyph, face, cursorRect, cell, v.Theme.CursorForeground)
					}
				}
			}
		}
		if focused && w.composition != "" {
			face := faces[0]
			width, _ := text.Measure(w.composition, face, 0)
			bounds := image.Rect(x, y, min(r.Max.X, x+int(width)+w.dp(4)), y+lh)
			nativeRect(dst, bounds, v.Theme.Background)
			op := &text.DrawOptions{}
			op.GeoM.Translate(float64(x), float64(y)+cell.baseline-face.Metrics().HAscent)
			op.ColorScale.ScaleWithColor(v.Theme.Foreground)
			text.Draw(dst, w.composition, face, op)
			nativeRect(dst, image.Rect(x, y+lh-1, bounds.Max.X, y+lh), v.Theme.Foreground)
		}
	}
}
