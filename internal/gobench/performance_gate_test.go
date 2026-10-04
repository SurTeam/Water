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

const (
	performanceGateBytes              = 32_000_000
	minimumSharedRunnerThroughputMBps = 5.0
)

type performanceSample struct {
	bytes      int64
	duration   time.Duration
	maxBacklog int
	queueCap   int
}

// Buffer the producer's writes without changing the byte stream. BSD yes
// writes one line at a time, making Darwin PTY syscall overhead dominate the
// emulator/transport measurement. dd combines those writes on both platforms.
func bufferedTerminalCommand(marker string, size int) string {
	return fmt.Sprintf("yes %s | head -c %d | dd obs=65536 2>/dev/null", marker, size)
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

	// The absolute floor only catches order-of-magnitude regressions. Shared
	// GitHub runners can vary enough that direct and server measurements may
	// even invert; compare performance on the same machine. This gate keeps
	// the architectural invariants hard: sequence
	// continuity, bounded queues/replay, server/direct retention, and latency.
	if direct.mbps() < minimumSharedRunnerThroughputMBps {
		t.Fatalf(
			"direct terminal throughput %.1f MB/s is below %.1f MB/s shared-runner floor",
			direct.mbps(), minimumSharedRunnerThroughputMBps,
		)
	}
	if server.mbps() < minimumSharedRunnerThroughputMBps {
		t.Fatalf(
			"server terminal throughput %.1f MB/s is below %.1f MB/s shared-runner floor",
			server.mbps(), minimumSharedRunnerThroughputMBps,
		)
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
		[]string{"-c", "read _; " + bufferedTerminalCommand("WATER_GO_PERF", performanceGateBytes)},
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
				if total < performanceGateBytes {
					t.Fatalf("direct terminal received %d bytes, want at least %d", total, performanceGateBytes)
				}
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
		"type":    "terminal.spawn",
		"program": "/bin/sh",
		"args":    []string{"-c", "read _; " + bufferedTerminalCommand("WATER_GO_PERF", performanceGateBytes)},
		"columns": 80,
		"lines":   24,
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
		LastSeq uint64                         `json:"last_seq"`
	}
	if err := session.Attach(spawned.TerminalID, &attached); err != nil {
		t.Fatal(err)
	}
	emu := govt.New(attached.Size.Columns, attached.Size.Lines, 10_000)
	defer emu.Close()

	lastSeq := attached.LastSeq
	if err := session.DispatchAsync(map[string]any{
		"type":        "terminal.send_text",
		"terminal_id": spawned.TerminalID,
		"text":        "\n",
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
				if total < performanceGateBytes {
					t.Fatalf("server terminal received %d bytes, want at least %d", total, performanceGateBytes)
				}
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

func TestTerminalInteractionPerformanceGate(t *testing.T) {
	if testing.Short() {
		t.Skip("performance gate is disabled in short mode")
	}

	socket := filepath.Join("/tmp", "water-go-interaction-"+uuid.New().String()+".sock")
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
		"type":    "terminal.spawn",
		"program": "/bin/sh",
		"args":    []string{"-c", "read _; exec yes WATER_INTERACTION_FLOOD"},
		"columns": 80,
		"lines":   24,
	}, &spawned); err != nil {
		t.Fatal(err)
	}

	session, err := client.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	var attached struct {
		LastSeq uint64 `json:"last_seq"`
	}
	if err := session.Attach(spawned.TerminalID, &attached); err != nil {
		t.Fatal(err)
	}
	if err := session.DispatchAsync(map[string]any{
		"type":        "terminal.send_text",
		"terminal_id": spawned.TerminalID,
		"text":        "\n",
	}); err != nil {
		t.Fatal(err)
	}

	lastSeq := attached.LastSeq
	waitDeadline := time.NewTimer(3 * time.Second)
	defer waitDeadline.Stop()
	for {
		select {
		case push := <-session.Events:
			if push.TerminalID != spawned.TerminalID || push.Event.Seq <= lastSeq {
				continue
			}
			if lastSeq != 0 && push.Event.Seq != lastSeq+1 {
				t.Fatalf("interaction stream sequence gap: previous=%d next=%d", lastSeq, push.Event.Seq)
			}
			lastSeq = push.Event.Seq
			if push.Event.Kind == goprotocol.OutputEvent && len(push.Event.Data) > 1024 {
				goto floodReady
			}
		case <-waitDeadline.C:
			t.Fatal("terminal flood did not become ready")
		}
	}

floodReady:
	resizeStart := time.Now()
	if err := client.Dispatch(map[string]any{
		"type":        "terminal.resize",
		"terminal_id": spawned.TerminalID,
		"columns":     100,
		"lines":       31,
	}, nil); err != nil {
		t.Fatal(err)
	}
	resizeLatency := waitInteractionEvent(t, session, spawned.TerminalID, &lastSeq, func(ev goprotocol.TerminalEvent) bool {
		return ev.Kind == goprotocol.ResizeEvent && ev.Size.Columns == 100 && ev.Size.Lines == 31
	}, 2*time.Second, resizeStart)

	inputStart := time.Now()
	if err := client.Dispatch(map[string]any{
		"type":        "terminal.send_bytes",
		"terminal_id": spawned.TerminalID,
		"bytes":       []int{3},
	}, nil); err != nil {
		t.Fatal(err)
	}
	inputLatency := waitInteractionEvent(t, session, spawned.TerminalID, &lastSeq, func(ev goprotocol.TerminalEvent) bool {
		return ev.Kind == goprotocol.ExitEvent
	}, 2*time.Second, inputStart)

	t.Logf("terminal interaction: resize=%s ctrl-c-to-exit=%s", resizeLatency, inputLatency)
	if resizeLatency > 250*time.Millisecond {
		t.Fatalf("resize latency %s exceeds 250ms gate", resizeLatency)
	}
	if inputLatency > time.Second {
		t.Fatalf("Ctrl-C to exit latency %s exceeds 1s gate", inputLatency)
	}
}

func waitInteractionEvent(
	t *testing.T,
	session *goclient.Session,
	terminalID uuid.UUID,
	lastSeq *uint64,
	match func(goprotocol.TerminalEvent) bool,
	timeout time.Duration,
	start time.Time,
) time.Duration {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case push, ok := <-session.Events:
			if !ok {
				t.Fatal("interaction terminal stream closed")
			}
			if push.TerminalID != terminalID || push.Event.Seq <= *lastSeq {
				continue
			}
			if *lastSeq != 0 && push.Event.Seq != *lastSeq+1 {
				t.Fatalf("interaction stream sequence gap: previous=%d next=%d", *lastSeq, push.Event.Seq)
			}
			*lastSeq = push.Event.Seq
			if match(push.Event) {
				return time.Since(start)
			}
		case <-timer.C:
			t.Fatalf("timed out after %s waiting for interaction event", timeout)
		}
	}
}
