//go:build !windows

package gosupervisor

import (
	"bytes"
	"github.com/SurTeam/Water/internal/gopty"
	"os"
	"testing"
	"time"
)

func TestRawReadDrainPreservesFragmentedOutput(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	term := &terminal{ptmx: &gopty.Session{File: reader}}
	out := make(chan []byte, 64)
	free := make(chan []byte, 2)
	done := make(chan struct{})
	defer close(done)
	go term.rawReadLoop(out, free, done)
	want := make([]byte, 8*readBlock+317)
	for i := range want {
		want[i] = byte(i % 251)
	}
	writeErr := make(chan error, 1)
	go func() {
		for start := 0; start < len(want); {
			end := min(start+997, len(want))
			if _, err := writer.Write(want[start:end]); err != nil {
				writeErr <- err
				return
			}
			start = end
		}
		writeErr <- writer.Close()
	}()
	var got []byte
	deadline := time.After(5 * time.Second)
	for {
		select {
		case block, ok := <-out:
			if !ok {
				if err := <-writeErr; err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want) {
					t.Fatalf("fragmented output mismatch: got %d want %d", len(got), len(want))
				}
				return
			}
			if len(block) > readBlock || cap(block) != readBlock {
				t.Fatalf("unexpected block length/capacity: %d/%d", len(block), cap(block))
			}
			// Match readLoop ownership: copy before returning the block.
			got = append(got, block...)
			select {
			case free <- block[:readBlock]:
			default:
			}
		case <-deadline:
			t.Fatal("raw reader did not drain and finish")
		}
	}
}

func TestRawReadPartialBlockFlushesWhileWriterRemainsOpen(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	out := make(chan []byte, 1)
	done := make(chan struct{})
	defer close(done)
	go (&terminal{ptmx: &gopty.Session{File: reader}}).rawReadLoop(out, make(chan []byte, 2), done)
	if _, err := writer.Write([]byte("prompt> ")); err != nil {
		t.Fatal(err)
	}
	select {
	case block := <-out:
		if string(block) != "prompt> " {
			t.Fatalf("partial output %q", block)
		}
	case <-time.After(time.Second):
		t.Fatal("partial block waited for more output or EOF")
	}
	if _, err := writer.Write([]byte("next")); err != nil {
		t.Fatal(err)
	}
	select {
	case block := <-out:
		if string(block) != "next" {
			t.Fatalf("output after clearing deadline %q", block)
		}
	case <-time.After(time.Second):
		t.Fatal("reader did not resume after partial flush")
	}
}
