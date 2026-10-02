package gobench

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

const performanceGateBytes = 32_000_000

type performanceSample struct {
	bytes      int64
	duration   time.Duration
	maxBacklog int
	queueCap   int
}

func (s performanceSample) mbps() float64 {
	if s.duration <= 0 {
		return 0
	}
	return float64(s.bytes) / s.duration.Seconds() / 1e6
}

func TestTerminalPerformanceGate(t *testing.T) {
	if testing.Short() {
		t.Skip("performance gate is disabled in short mode")
	}

	direct := measureDirectPerformance(t)
	server := measureServerPerformance(t)
	retention := server.mbps() / direct.mbps()

	t.Logf(
		"terminal performance: direct=%.1f MB/s server=%.1f MB/s retention=%.1f%% direct_backlog=%d server_backlog=%d",
		direct.mbps(), server.mbps(), retention*100, direct.maxBacklog, server.maxBacklog,
	)

	// These thresholds are intentionally well below the historical same-machine
	// Rust measurements. They catch architectural regressions (JSON/base64 live
	// data, lost backpressure, pathological allocation) without treating shared
	// CI runner noise as a release failure.
	if direct.mbps() < 20 {
		t.Fatalf("direct terminal throughput %.1f MB/s is below 20 MB/s", direct.mbps())
	}
	if server.mbps() < 20 {
		t.Fatalf("server terminal throughput %.1f MB/s is below 20 MB/s", server.mbps())
	}
	if retention < 0.60 {
		t.Fatalf("server/direct throughput retention %.1f%% is below 60%%", retention*100)
	}
	if direct.queueCap != 64 {
		t.Fatalf("terminal worker queue cap = %d, want 64", direct.queueCap)
	}
	if server.queueCap != 64 {
		t.Fatalf("client terminal queue cap = %d, want 64", server.queueCap)
	}
}

func measureDirectPerformance(t *testing.T) performanceSample {
	t.Helper()
	registry := goterminal.NewRegistry()
	defer registry.CloseAll()

	term, err := registry.Spawn(
		"/bin/sh",
		[]string{"-c", fmt.Sprintf("read _; yes WATER_GO_PERF | head -c %d", performanceGateBytes)},
		goprotocol.TerminalSize{Columns: 80, Lines: 24},
	)
	if err != nil {
		t.Fatal(err)
	}
	events, done, cancel := term.Subscribe()
	defer cancel()
	if cap(events) != 64 {
		t.Fatalf("terminal subscriber queue cap = %d, want 64", cap(events))
	}

	emu := govt.New(80, 24, 10_000)
	defer emu.Close()
	if err := term.Write([]byte("\n")); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	var total int64
	var previous uint64
	maxBacklog := 0
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	for {
		if queued := len(events); queued > maxBacklog {
			maxBacklog = queued
		}
		select {
		case ev := <-events:
			if previous != 0 && ev.Seq != previous+1 {
				t.Fatalf("direct terminal sequence gap: previous=%d next=%d", previous, ev.Seq)
			}
			previous = ev.Seq
			switch ev.Kind {
			case goprotocol.OutputEvent:
				total += int64(len(ev.Data))
				emu.Write(ev.Data)
			case goprotocol.ResizeEvent:
				emu.Resize(ev.Size.Columns, ev.Size.Lines)
			case goprotocol.ExitEvent:
				_ = emu.Snapshot()
				return performanceSample{
					bytes: total, duration: time.Since(start),
					maxBacklog: maxBacklog, queueCap: cap(events),
				}
			}
		case <-done:
			t.Fatal("direct terminal subscription ended before exit")
		case <-timer.C:
			t.Fatal("direct terminal performance gate timed out")
		}
	}
}

func measureServerPerformance(t *testing.T) performanceSample {
	t.Helper()
	socket := filepath.Join("/tmp", "water-go-perf-"+uuid.New().String()+".sock")
	server := goserver.New(socket)
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.ListenAndServe() }()
	defer func() {
		_ = server.Close()
		select {
		case <-serveDone:
		case <-time.After(time.Second):
		}
	}()

	client := goclient.New(socket)
	waitPerformanceServer(t, client)

	var spawned struct {
		TerminalID uuid.UUID `json:"terminal_id"`
	}
	if err := client.Dispatch(map[string]any{
		"type": "terminal.spawn",
		"program": "/bin/sh",
		"args": []string{"-c", fmt.Sprintf("read _; yes WATER_GO_PERF | head -c %d", performanceGateBytes)},
		"columns": 80,
		"lines": 24,
	}, &spawned); err != nil {
		t.Fatal(err)
	}

	session, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if cap(session.Events) != 64 {
		t.Fatalf("client terminal queue cap = %d, want 64", cap(session.Events))
	}

	var attached struct {
		Size    goprotocol.TerminalSize        `json:"size"`
		Replay  []goprotocol.WireTerminalEvent `json:"replay"`
		LastSeq uint64                          `json:"last_seq"`
	}
	if err := session.Attach(spawned.TerminalID, &attached); err != nil {
		t.Fatal(err)
	}
	emu := govt.New(attached.Size.Columns, attached.Size.Lines, 10_000)
	defer emu.Close()

	lastSeq := attached.LastSeq
	if err := session.DispatchAsync(map[string]any{
		"type": "terminal.send_text",
		"terminal_id": spawned.TerminalID,
		"text": "\n",
	}); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	var total int64
	maxBacklog := 0
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()

	for {
		if queued := len(session.Events); queued > maxBacklog {
			maxBacklog = queued
		}
		select {
		case push, ok := <-session.Events:
			if !ok {
				t.Fatal("server terminal stream closed before exit")
			}
			if push.TerminalID != spawned.TerminalID || push.Event.Seq <= attached.LastSeq {
				continue
			}
			if lastSeq != 0 && push.Event.Seq != lastSeq+1 {
				t.Fatalf("server terminal sequence gap: previous=%d next=%d", lastSeq, push.Event.Seq)
			}
			lastSeq = push.Event.Seq
			switch push.Event.Kind {
			case goprotocol.OutputEvent:
				total += int64(len(push.Event.Data))
				emu.Write(push.Event.Data)
			case goprotocol.ResizeEvent:
				emu.Resize(push.Event.Size.Columns, push.Event.Size.Lines)
			case goprotocol.ExitEvent:
				_ = emu.Snapshot()
				var metrics map[string]any
				if err := client.Call("debug.metrics", map[string]any{}, &metrics); err != nil {
					t.Fatal(err)
				}
				if retained := numberMetric(metrics, "replay_ring_bytes"); retained > 8*1024*1024 {
					t.Fatalf("replay ring retained %.0f bytes, exceeds 8 MiB", retained)
				}
				return performanceSample{
					bytes: total, duration: time.Since(start),
					maxBacklog: maxBacklog, queueCap: cap(session.Events),
				}
			}
		case <-timer.C:
			t.Fatal("server terminal performance gate timed out")
		}
	}
}

func waitPerformanceServer(t *testing.T, client *goclient.Client) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var pong any
		if client.Call("ping", map[string]any{}, &pong) == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("performance server did not become ready")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func numberMetric(metrics map[string]any, key string) float64 {
	switch value := metrics[key].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	case uint64:
		return float64(value)
	default:
		return 0
	}
}
