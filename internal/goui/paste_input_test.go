package goui

import (
	"bytes"
	"strings"
	"testing"
)

func TestNextPasteChunkPrefersNewline(t *testing.T) {
	data := []byte(strings.Repeat("a", 40) + "\n" + strings.Repeat("b", 40) + "\n" + strings.Repeat("c", 40))
	chunk, rest := nextPasteChunk(data, 60)
	if !bytes.HasSuffix(chunk, []byte("\n")) {
		t.Fatalf("chunk=%q", chunk)
	}
	if !bytes.HasPrefix(rest, []byte("b")) {
		t.Fatalf("rest=%q", rest)
	}
	chunk, rest = nextPasteChunk([]byte(strings.Repeat("x", 80)), 50)
	if len(chunk) != 50 || len(rest) != 30 {
		t.Fatalf("no-newline chunk=%d rest=%d", len(chunk), len(rest))
	}
}

func TestIsBracketedPastePayload(t *testing.T) {
	if !isBracketedPastePayload([]byte("\x1b[200~hello\x1b[201~")) {
		t.Fatal("expected bracketed")
	}
	if isBracketedPastePayload([]byte("hello")) {
		t.Fatal("plain paste marked bracketed")
	}
}

func TestOrdinaryPasteChunkingStaysUnderCanonLimit(t *testing.T) {
	payload := bytes.Repeat([]byte("line-content-\n"), 200) // well over MAX_INPUT
	if len(payload) <= pasteCanonChunk {
		t.Fatalf("payload too small: %d", len(payload))
	}
	var total int
	chunks := 0
	for rest := payload; len(rest) > 0; {
		chunk, next := nextPasteChunk(rest, pasteCanonChunk)
		if len(chunk) == 0 || len(chunk) > pasteCanonChunk {
			t.Fatalf("bad chunk len=%d", len(chunk))
		}
		total += len(chunk)
		chunks++
		rest = next
	}
	if total != len(payload) {
		t.Fatalf("chunked %d of %d", total, len(payload))
	}
	if chunks < 2 {
		t.Fatalf("expected paced chunks, got %d", chunks)
	}
}
