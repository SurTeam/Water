package goui

import (
	"github.com/google/uuid"
	"testing"
)

func TestNearestPaneSplit(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	root := &paneTree{Type: "split", Axis: "horizontal", First: &paneTree{Type: "leaf", PaneID: a}, Second: &paneTree{Type: "split", Axis: "vertical", First: &paneTree{Type: "leaf", PaneID: b}, Second: &paneTree{Type: "leaf", PaneID: c}}}
	for _, tc := range []struct {
		pane          uuid.UUID
		vertical      bool
		depth         int
		second, found bool
	}{
		{a, false, 0, false, true}, {b, false, 0, true, true}, {c, false, 0, true, true},
		{a, true, 0, false, false}, {b, true, 1, false, true}, {c, true, 1, true, true},
		{uuid.New(), false, 0, false, false},
	} {
		path, second, found := nearestPaneSplit(root, tc.pane, tc.vertical, nil)
		if found != tc.found || found && (len(path) != tc.depth || second != tc.second || tc.depth > 0 && !path[0]) {
			t.Fatalf("%+v: path=%v second=%v found=%v", tc, path, second, found)
		}
	}
}
