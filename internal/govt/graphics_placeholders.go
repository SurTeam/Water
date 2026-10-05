package govt

import "strings"

// Each run clips a full-size image to the cells that actually contain its
// placeholders. A viewport showing the bottom half must not rescale the image.
func (g *graphicsState) placeholderSnapshot(id uint32, record *imageRecord, rows []Row) []TerminalImage {
	width, height := record.placeholderSize[0], record.placeholderSize[1]
	if width <= 0 {
		width = ceilDiv(record.width, g.cellWidth)
	}
	if height <= 0 {
		height = ceilDiv(record.height, g.cellHeight)
	}
	var out []TerminalImage
	for y, row := range rows {
		previousRow, previousCol, previousID := -1, -1, uint32(0)
		for x, cell := range row.Cells {
			if !strings.ContainsRune(cell.Text, terminalImagePlaceholder) {
				previousRow, previousCol = -1, -1
				continue
			}
			var values [3]int
			count := 0
			for _, r := range cell.Text {
				if v, ok := imageDiacriticIndex(r); ok {
					values[count] = v
					count++
					if count == len(values) {
						break
					}
				}
			}
			imageID := placeholderImageID(cell.FG)
			imageRow, imageCol := 0, 0
			if count > 0 {
				imageRow = values[0]
			}
			if count > 1 {
				imageCol = values[1]
			}
			if count > 2 {
				imageID |= uint32(values[2]&255) << 24
			}
			if count == 0 && imageID == previousID&0xffffff && previousRow >= 0 {
				imageRow, imageCol, imageID = previousRow, previousCol+1, previousID
			} else if count == 1 && imageRow == previousRow && imageID == previousID&0xffffff {
				imageCol, imageID = previousCol+1, previousID
			} else if count == 2 && imageRow == previousRow && imageCol == previousCol+1 && imageID == previousID&0xffffff {
				imageID = previousID
			}
			previousRow, previousCol, previousID = imageRow, imageCol, imageID
			if imageID != id || imageRow >= height || imageCol >= width {
				continue
			}
			graphic := terminalImageFromRecord(record, y-imageRow, x-imageCol, width, height, nil)
			graphic.ClipRow, graphic.ClipColumn, graphic.ClipWidth, graphic.ClipHeight = y, x, 1, 1
			if n := len(out); n > 0 && out[n-1].Row == graphic.Row && out[n-1].Column == graphic.Column && out[n-1].ClipRow == y && out[n-1].ClipColumn+out[n-1].ClipWidth == x {
				out[n-1].ClipWidth++
			} else {
				out = append(out, graphic)
			}
		}
	}
	return out
}
