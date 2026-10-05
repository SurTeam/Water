package goui

import (
	"strings"
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func BenchmarkPrepareTerminalRow(b *testing.B) {
	for _, size := range []struct {
		name          string
		columns, text int
	}{{"Sparse", 160, 20}, {"Dense", 160, 160}, {"Wide", 512, 512}} {
		b.Run(size.name, func(b *testing.B) {
			e := govt.New(size.columns, 2, 100)
			defer e.Close()
			e.Write([]byte(strings.Repeat("x", size.text)))
			row := e.FrameSnapshot().RowsData[0]
			v := NewTerminalView()
			var prepared preparedRow
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				prepared = v.prepareRowInto(row, prepared)
			}
		})
	}
}
