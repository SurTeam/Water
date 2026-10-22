package gosupervisor

import (
	"bytes"
	"crypto/sha256"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

// Opt-in real cat benchmark shared with the GUI's testTermCat workload.
func TestCatBenchmark(t *testing.T) {
	path := os.Getenv("WATER_CAT_BENCHMARK_FILE")
	if path == "" {
		t.Skip("set WATER_CAT_BENCHMARK_FILE to the testTermCat fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Raw mode makes byte-for-byte validation independent of Darwin's PTY
	// newline expansion. In OPOST mode compare with CR removed: Darwin can
	// duplicate CR around output-buffer boundaries. All other bytes and their
	// order must still match the fixture hash.
	opost := os.Getenv("WATER_CAT_BENCHMARK_OPOST") != ""
	stty := "/bin/stty -echo -opost"
	expected := data
	if opost {
		stty = "/bin/stty -echo"
		expected = bytes.ReplaceAll(data, []byte{'\r'}, nil)
	}
	wantHash := sha256.Sum256(expected)
	_, socket := startSupervisor(t)
	client, err := Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id := uuid.New()
	_, _, err = client.Spawn(SpawnSpec{ID: id, Program: "/bin/sh", Args: []string{"-c", stty + "; printf READY; read trigger; /bin/bash -c 'time cat \"$1\"' benchmark \"$1\"; printf '\\nCAT_BENCH_DONE\\n'; read hold", "benchmark", path}, Env: os.Environ(), Size: struct{ Lines, Columns uint16 }{24, 80}})
	if err != nil {
		t.Fatal(err)
	}
	_, events, err := client.Attach(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	var ready []byte
	for !bytes.Contains(ready, []byte("READY")) {
		select {
		case ev := <-events:
			ready = append(ready, ev.Output...)
		case <-time.After(5 * time.Second):
			t.Fatal("no ready")
		}
	}
	start := time.Now()
	if err := client.Write(id, []byte("go\n")); err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	received, count, wireBytes := 0, 0, 0
	var trailer []byte
	deadline := time.After(20 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream closed")
			}
			if ev.Kind != EventOutput {
				continue
			}
			count++
			wireBytes += len(ev.Output)
			block := ev.Output
			if opost {
				block = bytes.ReplaceAll(block, []byte{'\r'}, nil)
			}
			n := min(len(block), len(expected)-received)
			hash.Write(block[:n])
			received += n
			trailer = append(trailer, block[n:]...)
			if bytes.Contains(trailer, []byte("CAT_BENCH_DONE")) {
				if !bytes.Equal(hash.Sum(nil), wantHash[:]) {

					t.Fatal("cat payload hash mismatch")
				}
				t.Logf("payload=%d validated=%d wire=%d elapsed=%s events=%d average=%.0f bytes original_time=%q", len(data), received, wireBytes, time.Since(start), count, float64(wireBytes)/float64(count), trailer)
				return
			}
		case <-deadline:
			t.Fatalf("timeout after %d bytes", received)
		}
	}
}
