package xterm

import (
	"os"
	"testing"
)

func BenchmarkANSIFile(b *testing.B) {
	path := os.Getenv("WATER_CAT_BENCHMARK_FILE")
	if path == "" {
		b.Skip("set WATER_CAT_BENCHMARK_FILE")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		b.Fatal(err)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		term := New(WithCols(80), WithRows(24), WithScrollback(10000))
		for start := 0; start < len(data); start += 128 * 1024 {
			end := min(start+128*1024, len(data))
			term.Write(data[start:end])
		}
		term.Dispose()
	}
}

// Screen-reader mode deliberately takes the general Print path, providing an
// independent cell-writing implementation for the ASCII bulk path.
func TestANSIPrintBulkMatchesGeneralPath(t *testing.T) {
	fast := New(WithCols(17), WithRows(4), WithScrollback(100))
	slow := New(WithCols(17), WithRows(4), WithScrollback(100), WithScreenReaderMode(true))
	defer fast.Dispose()
	defer slow.Dispose()
	chunks := []string{
		"\x1b[31mred text\x1b[0m normal\r\n",
		"界界\rX\x1b[1mabcdef012345678901234567890\x1b[0m\r\n",
		"\x1b[2;1H界\x1b[2;2HASCII\x1b[K",
		"\x1b[3;1H界\x1b[3;1HA\x1b[3;2HB",
		"\x1b[4;1Habcdefghijklmnopq\x1b[32mZ\x1b[0m",
	}
	for i := 0; i < 30; i++ {
		for _, chunk := range chunks {
			fast.Write([]byte(chunk))
			slow.Write([]byte(chunk))
			fb, sb := fast.Buffer(), slow.Buffer()
			if fb.X != sb.X || fb.Y != sb.Y || fb.YBase != sb.YBase || fb.Lines.Length() != sb.Lines.Length() {
				t.Fatalf("geometry mismatch after %q", chunk)
			}
			for row := 0; row < fb.Lines.Length(); row++ {
				fl, sl := fb.Lines.Get(row), sb.Lines.Get(row)
				if fl.IsWrapped != sl.IsWrapped {
					t.Fatalf("wrap mismatch row %d", row)
				}
				for col := 0; col < 17; col++ {
					var fc, sc CellData
					fl.LoadCell(col, &fc)
					sl.LoadCell(col, &sc)
					if fc.Content != sc.Content || fc.Fg != sc.Fg || fc.Bg != sc.Bg || fc.GetChars() != sc.GetChars() {
						t.Fatalf("cell mismatch row=%d col=%d after %q: %+v != %+v", row, col, chunk, fc, sc)
					}
				}
			}
		}
	}
}
