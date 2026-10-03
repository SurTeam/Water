package govt

import "testing"

func TestPrivateUseIconAgreesWithShellColumnsAndWrap(t *testing.T) {
	e := New(4, 3, 0)
	defer e.Close()
	e.Write([]byte("abZ"))
	s := e.Snapshot()
	if s.RowsData[0].Cells[2].Text != "" || s.RowsData[0].Cells[2].Width != 1 || s.RowsData[0].Cells[3].Text != "Z" || s.CursorY != 0 {
		t.Fatalf("prompt wrapped despite fitting four logical columns: %+v", s)
	}
	e.Write([]byte("!"))
	s = e.Snapshot()
	if s.RowsData[1].Cells[0].Text != "!" || !s.RowsData[1].Wrapped || s.CursorX != 1 || s.CursorY != 1 {
		t.Fatal("wrapping does not match logical columns")
	}
}
