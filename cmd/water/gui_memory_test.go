package main

import "testing"

func TestGUIMemoryBudgetLeavesRoomForLiveFonts(t *testing.T) {
	for _, tc := range []struct {
		live uint64
		want int64
	}{{0, 192 << 20}, {100 << 20, 192 << 20}, {160 << 20, 224 << 20}, {300 << 20, 364 << 20}, {^uint64(0), 1<<63 - 1}} {
		if got := guiMemoryBudget(tc.live); got != tc.want {
			t.Fatalf("live=%d: budget=%d want=%d", tc.live, got, tc.want)
		}
	}
}
