package xterm

import (
	"fmt"
	"strings"
	"testing"
)

func TestBufferResizeSmallerRetainsNewestLines(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cols       int
		newCols    int
		scrollback int
		paragraphs []string
	}{
		{"single_insert_over_capacity", 24, 1, 1, []string{"abcdefghijklmnopqrstuvwx"}},
		{"multiple_inserts_over_capacity", 8, 1, 1, []string{"abcdefgh", "ijklmnop"}},
		{"partial_final_lines", 8, 3, 1, []string{"abcdefgh", "ijklmnop"}},
		{"wrapped_paragraph", 8, 2, 1, []string{"abcdefghijklmnop"}},
		{"within_capacity", 8, 4, 10, []string{"abcdefgh", "ijklmnop"}},
	} {
		for _, rotation := range []int{0, 1, 2} {
			t.Run(fmt.Sprintf("%s/rotation_%d", tc.name, rotation), func(t *testing.T) {
				b := NewBuffer(BufferOptions{Cols: tc.cols, Rows: 2, Scrollback: tc.scrollback, HasScrollback: true})
				// Advance the circular start as well as testing an unrotated buffer.
				for range rotation {
					b.Lines.Push(b.GetBlankLine(nil, false))
				}
				var want []string
				originalLines := 1 // Separate cursor line below.
				for _, paragraph := range tc.paragraphs {
					originalLines += (len(paragraph) + tc.cols - 1) / tc.cols
					for start := 0; start < len(paragraph); start += tc.cols {
						line := b.GetBlankLine(nil, start > 0)
						text := paragraph[start:min(start+tc.cols, len(paragraph))]
						for col, ch := range text {
							line.SetCellFromCodepoint(col, uint32(ch), 1, &AttributeData{})
						}
						b.Lines.Push(line)
					}
					for start := 0; start < len(paragraph); start += tc.newCols {
						want = append(want, paragraph[start:min(start+tc.newCols, len(paragraph))])
					}
				}
				// Keep the cursor on a separate blank line so all text is reflowed.
				b.Lines.Push(b.GetBlankLine(nil, false))
				b.YBase = b.Lines.Length() - 2
				b.YDisp = b.YBase
				b.Y = 1
				want = append(want, "")
				// Include only the leading blank rows retained before reflow.
				leadingBlanks := min(rotation, b.Lines.MaxLength()-originalLines)
				want = append(make([]string, leadingBlanks), want...)
				trimmed := 0
				b.Lines.OnTrimEmitter.Event(func(n int) { trimmed += n })
				wantTrimmed := max(0, len(want)-b.Lines.MaxLength())
				want = want[wantTrimmed:]

				b.Resize(tc.newCols, 2)

				if got := b.Lines.Length(); got != len(want) {
					t.Fatalf("line count = %d, want %d", got, len(want))
				}
				if trimmed != wantTrimmed {
					t.Errorf("trimmed = %d, want %d", trimmed, wantTrimmed)
				}
				for i, text := range want {
					if b.Lines.Get(i) == nil {
						t.Fatalf("line %d is nil", i)
					}
					if got := b.TranslateBufferLineToString(i, true, 0, tc.newCols); got != text {
						t.Errorf("line %d = %q, want %q", i, got, text)
					}
				}
				if got := b.TranslateBufferLineToString(b.YBase+b.Y, true, 0, tc.newCols); strings.TrimSpace(got) != "" {
					t.Errorf("cursor line = %q, want blank", got)
				}
			})
		}
	}
}
