package gosupervisor

import (
	"bytes"
	"errors"
	"testing"
)

type shortWriter struct {
	remaining int
	maxChunk  int
	writes    int
}

func (w *shortWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.remaining <= 0 {
		return 0, errors.New("write limit reached")
	}
	if w.maxChunk > 0 && len(p) > w.maxChunk {
		p = p[:w.maxChunk]
	}
	if len(p) > w.remaining {
		p = p[:w.remaining]
	}
	w.remaining -= len(p)
	return len(p), nil
}

func TestWriteFrameCompletesShortWrites(t *testing.T) {
	writer := &shortWriter{remaining: 1 << 30, maxChunk: 7}
	if err := writeFrame(writer, kindWriteBinary, bytes.Repeat([]byte("x"), 64)); err != nil {
		t.Fatal(err)
	}
	if writer.writes < 2 {
		t.Fatalf("writeFrame did not retry a short write: %d", writer.writes)
	}
}

func TestWriteFrameStopsOnWriterFailure(t *testing.T) {
	writer := &shortWriter{remaining: 3}
	if err := writeFrame(writer, kindWriteBinary, bytes.Repeat([]byte("x"), 64)); err == nil {
		t.Fatal("writeFrame ignored a short writer failure")
	}
}
