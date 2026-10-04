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
