package govt

import (
	"errors"
	"strings"
)

const (
	MaxContentRows  = 256
	MaxContentBytes = 1024 * 1024
)

// TextContent describes physical rows from the client's active terminal buffer.
// StartRow is absolute within the retained buffer, not relative to the viewport.
type TextContent struct {
	StartRow           int      `json:"start_row"`
	Rows               int      `json:"rows"`
	Columns            int      `json:"columns"`
	TotalRows          int      `json:"total_rows"`
	YBase              int      `json:"y_base"`
	YDisp              int      `json:"y_disp"`
	TrimmedLines       uint64   `json:"trimmed_lines"`
	AltScreen          bool     `json:"alt_screen"`
	SynchronizedOutput bool     `json:"synchronized_output"`
	Lines              []string `json:"lines"`
	Text               string   `json:"text"`
}

// ReadContent reads a bounded range without scrolling, selecting, publishing a
// frame or generating terminal replies. Nil startRow means the current viewport.
func (e *Emulator) ReadContent(startRow *int, rows *int) (TextContent, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	buf := e.term.Buffer()
	start, count := buf.YDisp, e.term.Rows()
	if startRow != nil {
		start = *startRow
	}
	if rows != nil {
		count = *rows
	}
	if start < 0 || start > buf.Lines.Length() {
		return TextContent{}, errors.New("start_row is outside retained terminal content")
	}
	if count < 1 || count > MaxContentRows {
		return TextContent{}, errors.New("rows must be between 1 and 256")
	}
	count = min(count, buf.Lines.Length()-start)
	out := TextContent{
		StartRow: start, Rows: count, Columns: e.term.Cols(),
		TotalRows: buf.Lines.Length(), YBase: buf.YBase, YDisp: buf.YDisp,
		TrimmedLines: e.trimmedLines, AltScreen: e.term.IsAltBufferActive(),
		SynchronizedOutput: e.term.DecPrivateModes().SynchronizedOutput,
		Lines:              make([]string, 0, count),
	}
	bytes := 0
	for row := start; row < start+count; row++ {
		line := buf.Lines.Get(row)
		text := ""
		if line != nil {
			// Check cell text sizes before allocating a potentially large line.
			for col := 0; col < min(line.Len, out.Columns); col++ {
				bytes += max(1, len(line.GetString(col)))
				if bytes > MaxContentBytes {
					return TextContent{}, errors.New("terminal content exceeds 1 MiB; request fewer rows")
				}
			}
			text = strings.TrimRight(line.TranslateToString(true, 0, out.Columns), " ")
		}
		out.Lines = append(out.Lines, text)
	}
	out.Text = strings.Join(out.Lines, "\n")
	return out, nil
}
