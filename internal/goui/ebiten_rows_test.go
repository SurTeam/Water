package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/govt"
)

func TestNativeRowsFollowScrolledContent(t *testing.T) {
	old := map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 20}, 2: {hash: 30}}
	matches, unused := matchNativeRows([]govt.Row{{Hash: 20}, {Hash: 30}, {Hash: 40}}, old)
	if matches[0] != 1 || matches[1] != 2 || matches[2] != -1 || len(unused) != 1 || unused[0] != 0 {
		t.Fatalf("scrolled matches=%v unused=%v", matches, unused)
	}
}

func TestNativeRowsConsumeDuplicateHashesOnce(t *testing.T) {
	old := map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 10}, 2: {hash: 20}}
	matches, unused := matchNativeRows([]govt.Row{{Hash: 10}, {Hash: 10}, {Hash: 10}}, old)
	if matches[0] < 0 || matches[1] < 0 || matches[0] == matches[1] || matches[2] != -1 || len(unused) != 1 || unused[0] != 2 {
		t.Fatalf("duplicate matches=%v unused=%v", matches, unused)
	}
}

func TestRecycleNativeRowsUnchanged(t *testing.T) {
	// Duplicate hashes must retain their original slots on an unchanged draw.
	cache := nativeTerminalTexture{rows: map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 10}, 2: {hash: 0}}}
	v := &TerminalView{cache: map[int]preparedRow{0: {hash: 10}, 1: {hash: 10}, 2: {hash: 0}}}
	rows := []govt.Row{{Hash: 10}, {Hash: 10}, {Hash: 0}}
	oldTextures, oldPrepared := cache.rows, v.cache
	if allocations := testing.AllocsPerRun(100, func() { recycleNativeRows(&cache, v, rows) }); allocations != 0 {
		t.Fatalf("unchanged rows allocated %g times", allocations)
	}
	// Mutations through the original maps prove that neither map was replaced.
	oldTextures[0] = nativeRowTexture{hash: 99}
	oldPrepared[0] = preparedRow{hash: 99}
	if cache.rows[0].hash != 99 || v.cache[0].hash != 99 {
		t.Fatal("unchanged rows replaced cache maps")
	}
}

func TestRecycleNativeRowsFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		old  map[int]nativeRowTexture
		rows []govt.Row
		want []uint64
	}{
		{"changed", map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 20}}, []govt.Row{{Hash: 10}, {Hash: 30}}, []uint64{10, ^uint64(30)}},
		{"scroll", map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 20}, 2: {hash: 30}}, []govt.Row{{Hash: 20}, {Hash: 30}, {Hash: 40}}, []uint64{20, 30, ^uint64(40)}},
		{"duplicate", map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 10}, 2: {hash: 20}}, []govt.Row{{Hash: 10}, {Hash: 10}, {Hash: 10}}, []uint64{10, 10, ^uint64(10)}},
		{"shrink", map[int]nativeRowTexture{0: {hash: 10}, 1: {hash: 20}}, []govt.Row{{Hash: 10}}, []uint64{10}},
		{"missing-zero-slot", map[int]nativeRowTexture{1: {hash: 0}}, []govt.Row{{Hash: 0}}, []uint64{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := nativeTerminalTexture{rows: tc.old}
			v := &TerminalView{cache: make(map[int]preparedRow)}
			for y, row := range tc.old {
				v.cache[y] = preparedRow{hash: row.hash}
			}
			recycleNativeRows(&cache, v, tc.rows)
			if len(cache.rows) != len(tc.rows) || len(v.cache) != len(tc.rows) {
				t.Fatalf("wrong cache sizes: %d, %d", len(cache.rows), len(v.cache))
			}
			for y, want := range tc.want {
				if _, ok := cache.rows[y]; !ok {
					t.Fatalf("missing texture slot %d", y)
				}
				if got, ok := v.cache[y]; !ok || got.hash != want {
					t.Fatalf("prepared slot %d = %+v, present %t; want hash %d", y, got, ok, want)
				}
			}
		})
	}
}

func BenchmarkRecycleNativeRows(b *testing.B) {
	for _, mode := range []string{"unchanged", "changed", "scroll"} {
		b.Run(mode, func(b *testing.B) {
			const count = 40
			cache := nativeTerminalTexture{rows: make(map[int]nativeRowTexture, count)}
			v := &TerminalView{cache: make(map[int]preparedRow, count)}
			rows := make([]govt.Row, count)
			for y := range rows {
				rows[y].Hash = uint64(y + 1)
				cache.rows[y] = nativeRowTexture{hash: rows[y].Hash}
				v.cache[y] = preparedRow{hash: rows[y].Hash}
			}
			if mode == "changed" {
				rows[count-1].Hash = 100
			}
			if mode == "scroll" {
				for y := range rows {
					rows[y].Hash++
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				recycleNativeRows(&cache, v, rows)
			}
		})
	}
}
