package goterminal

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

const MaxExecuteCommandBytes = 16 * 1024

// ExecuteOptions bounds both execution time and returned rendered content.
type ExecuteOptions struct {
	Command  string
	Timeout  time.Duration
	MaxRows  int
	MaxBytes int
}

type ExecuteResult struct {
	Text      string   `json:"text"`
	Lines     []string `json:"lines"`
	ExitCode  *int     `json:"exit_code,omitempty"`
	TimedOut  bool     `json:"timed_out"`
	Signals   []string `json:"signals"`
	Truncated bool     `json:"truncated"`
	AltScreen bool     `json:"alt_screen"`
}

// Execute runs a foreground child through an idle POSIX interactive shell.
// It reconstructs retained replay privately; it does not persist a server screen.
func (t *Terminal) Execute(ctx context.Context, opts ExecuteOptions) (ExecuteResult, error) {
	var result ExecuteResult
	if len(opts.Command) == 0 || len(opts.Command) > MaxExecuteCommandBytes || strings.IndexByte(opts.Command, 0) >= 0 {
		return result, errors.New("command must contain 1..16384 bytes without NUL")
	}
	if opts.Timeout == 0 {
		opts.Timeout = 30 * time.Second
	}
	if opts.MaxRows == 0 {
		opts.MaxRows = 256
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = 1024 * 1024
	}
	if opts.Timeout < time.Millisecond || opts.Timeout > 10*time.Minute || opts.MaxRows < 1 || opts.MaxRows > govt.MaxContentRows || opts.MaxBytes < 1 || opts.MaxBytes > govt.MaxContentBytes {
		return result, errors.New("execution bounds out of range")
	}
	if !t.executeMu.TryLock() {
		return result, errors.New("terminal execution already in progress")
	}
	defer t.executeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !t.interactivePOSIXShell() {
		return result, errors.New("execution requires an interactive POSIX shell (sh, bash, dash, ksh, or zsh)")
	}
	shellPID := t.cmd.Process.Pid
	if fg, err := t.ForegroundGroup(); err != nil || fg != shellPID {
		return result, errors.New("terminal shell is not idle in the foreground")
	}
	events, done, cancel := t.Subscribe()
	defer cancel()
	size := t.Size()
	em := govt.New(size.Columns, size.Lines, 10000)
	defer em.Close()
	var seq uint64
	apply := func(ev goprotocol.TerminalEvent) {
		switch ev.Kind {
		case goprotocol.OutputEvent:
			em.WriteReplay(ev.Data)
		case goprotocol.ResizeEvent:
			em.Resize(ev.Size.Columns, ev.Size.Lines)
		}
	}
	for _, ev := range t.Replay() {
		apply(ev)
		seq = max(seq, ev.Seq)
	}
	// The marker prefix contains no literal ESC in the submitted command, so PTY
	// input echo cannot be mistaken for a start marker.
	token := "water-exec-" + uuid.NewString()
	prefix := []byte("\x1b]777;" + token + ";")
	var start, end govt.ContentCursor
	started, ended := false, false
	rangeInvalid := false
	var scanTail []byte
	pid := 0
	var identity goagent.Process
	parser := executeMarkerParser{prefix: prefix}
	parser.normal = func(p []byte) {
		if !ended {
			if started {
				scan := append(scanTail, p...)
				if executeRangeEscape.Match(scan) {
					rangeInvalid = true
				}
				if executeAltEscape.Match(scan) {
					rangeInvalid = true
					result.AltScreen = true
				}
				scanTail = append([]byte(nil), scan[max(0, len(scan)-64):]...)
			}
			em.WriteReplay(p)
			if started && em.ReadContentCursor().AltScreen {
				result.AltScreen = true
			}
		}
	}
	parser.marker = func(value string) {
		fields := strings.Split(value, ";")
		if len(fields) != 2 {
			return
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil {
			return
		}
		if fields[0] == "start" && !started && n > 1 && n != shellPID {
			pid = n
			started = true
			start = em.ReadContentCursor()
			sampleCtx, c := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer c()
			processes, _ := sampleProcessGroups(sampleCtx, []int{pid})
			identity = processes[pid]
		} else if fields[0] == "end" && started && !ended {
			ended = true
			end = em.ReadContentCursor()
			result.ExitCode = &n
		}
	}
	// Keep the group leader alive through TERM so an ignoring descendant can be
	// killed after the grace period, even when no end marker will ever arrive.
	script := "water_term=0; trap 'water_term=1' TERM; printf '\\033]777;" + token + ";start;%s\\007' \"$$\"; /bin/sh -c \"${1%x}\" <&0 & water_child=$!; wait \"$water_child\"; water_status=$?; while kill -0 \"$water_child\" 2>/dev/null; do wait \"$water_child\"; water_status=$?; done; while [ \"$water_term\" = 1 ]; do sleep 1; done; printf '\\033]777;" + token + ";end;%s\\007' \"$water_status\""
	line := "/bin/sh -c " + shellQuote(script) + " water-exec " + "\"$(printf '%s' " + shellQuote(base64.StdEncoding.EncodeToString([]byte(opts.Command))) + " | base64 -d; printf x)\"" + "\n"
	// Canonical PTYs have a small line limit. Assemble long commands through
	// short quoted assignments before evaluating them in the interactive shell.
	line = splitExecuteInput(line)
	if fg, err := t.ForegroundGroup(); err != nil || fg != shellPID {
		return result, errors.New("terminal foreground changed before execution")
	}
	// Feed canonical lines gradually while draining output below. A single burst
	// can overflow the terminal driver's input queue even with short lines.
	inputLines := strings.SplitAfter(strings.TrimSuffix(line, "\n"), "\n")
	inputLines[len(inputLines)-1] += "\n"
	inputTick := time.NewTicker(5 * time.Millisecond)
	defer inputTick.Stop()
	input := inputTick.C
	submissionDone := false
	deadline := time.NewTimer(opts.Timeout)
	defer deadline.Stop()
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var stopping, killAt, finishAt, returnedAt, endedAt time.Time
	ctxDone := ctx.Done()
	var executionErr error
	signal := func(sig syscall.Signal) {
		if pid <= 1 || pid == shellPID || identity.PID != pid || identity.PGID != pid || identity.Started == "" {
			return
		}
		fg, err := t.ForegroundGroup()
		if err != nil || fg != pid {
			return
		}
		sampleCtx, c := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer c()
		processes, err := sampleProcessGroups(sampleCtx, []int{pid})
		current := processes[pid]
		if err != nil || current.PID != pid || current.PGID != pid || current.Started != identity.Started {
			return
		}
		if fg, err = t.ForegroundGroup(); err != nil || fg != pid {
			return
		}
		if syscall.Kill(-pid, sig) == nil {
			if sig == syscall.SIGTERM {
				result.Signals = append(result.Signals, "TERM")
			} else {
				result.Signals = append(result.Signals, "KILL")
			}
		}
	}
	stop := func() {
		if stopping.IsZero() {
			stopping = time.Now()
			killAt = stopping.Add(250 * time.Millisecond)
			finishAt = stopping.Add(2 * time.Second)
			signal(syscall.SIGTERM)
		}
	}
	running := true
	for running {
		select {
		case <-input:
			if len(inputLines) == 0 {
				submissionDone = true
				inputTick.Stop()
				input = nil
				continue
			}
			if !stopping.IsZero() {
				inputLines = []string{"unset water_exec_input\n"}
			}
			if err := t.executeWrite([]byte(inputLines[0])); err != nil {
				executionErr = err
				// Discard any incomplete canonical line, then remove staged data.
				_ = t.executeWrite([]byte("\x15\nunset water_exec_input\n"))
				inputLines = nil
				stop()
			}
			if len(inputLines) > 0 {
				inputLines = inputLines[1:]
			}
			if len(inputLines) == 0 {
				submissionDone = true
				inputTick.Stop()
				input = nil
			}
		case ev := <-events:
			if ev.Seq <= seq {
				continue
			}
			if ev.Seq != seq+1 {
				executionErr = errors.New("terminal execution stream has a sequence gap")
				stop()
			}
			seq = ev.Seq
			switch ev.Kind {
			case goprotocol.OutputEvent:
				parser.write(ev.Data)
			case goprotocol.ResizeEvent:
				rangeInvalid = true
				size = ev.Size
				em.Resize(ev.Size.Columns, ev.Size.Lines)
			case goprotocol.ExitEvent:
				executionErr = errors.New("terminal shell exited during execution")
				running = false
			}
			if ended && endedAt.IsZero() {
				endedAt = time.Now()
			}
		case <-deadline.C:
			result.TimedOut = true
			stop()
		case <-ctxDone:
			executionErr = ctx.Err()
			ctxDone = nil
			stop()
		case now := <-tick.C:
			if ended && stopping.IsZero() {
				if fg, err := t.ForegroundGroup(); err == nil && fg == shellPID {
					running = false
				}
				if now.Sub(endedAt) >= time.Second {
					executionErr = errors.New("terminal foreground did not return after execution")
					stop()
				}
			}
			if !stopping.IsZero() {
				// Cancellation can precede the start marker. Once identity arrives,
				// still give the captured child its TERM grace period.
				if len(result.Signals) == 0 && pid > 1 {
					signal(syscall.SIGTERM)
					if len(result.Signals) > 0 {
						killAt = now.Add(250 * time.Millisecond)
					}
				}
				if !killAt.IsZero() && !now.Before(killAt) {
					signal(syscall.SIGKILL)
					killAt = time.Time{}
				}
				if fg, err := t.ForegroundGroup(); err == nil && fg == shellPID && submissionDone {
					if returnedAt.IsZero() {
						returnedAt = now
					}
					if now.Sub(returnedAt) >= 100*time.Millisecond {
						running = false
					}
				} else {
					returnedAt = time.Time{}
				}
				if !now.Before(finishAt) {
					if executionErr == nil {
						executionErr = errors.New("foreground child did not return to shell after timeout cleanup")
					}
					running = false
				}
			}
		case <-done:
			executionErr = errors.New("terminal subscription closed")
			running = false
		case <-t.closed:
			executionErr = errors.New("terminal closed during execution")
			running = false
		}
	}
	parser.flush()
	if !started {
		result.Truncated = true
		if executionErr == nil && stopping.IsZero() {
			executionErr = errors.New("execution start marker was not received")
		}
		return result, executionErr
	}
	if !ended {
		end = em.ReadContentCursor()
	}
	cursor := em.ReadContentCursor()
	result.AltScreen = result.AltScreen || cursor.AltScreen
	// Row identities are stable across primary scrollback trimming. Buffer
	// switches invalidate that identity; report the bounded active viewport.
	first, last := int64(start.TrimmedLines)+int64(start.Row), int64(end.TrimmedLines)+int64(end.Row)
	retained := int64(cursor.TrimmedLines)
	row := int(first - retained)
	count := int(last-first) + 1
	if rangeInvalid || start.AltScreen != cursor.AltScreen || end.AltScreen != cursor.AltScreen || last < first {
		row = max(0, cursor.TotalRows-size.Lines)
		count = size.Lines
		result.Truncated = true
	}
	if row < 0 {
		count += row
		row = 0
		result.Truncated = true
	}
	if count < 1 {
		count = 1
		result.Truncated = true
	}
	if count > opts.MaxRows {
		row += count - opts.MaxRows
		count = opts.MaxRows
		result.Truncated = true
	}
	if row >= cursor.TotalRows {
		row = max(0, cursor.TotalRows-1)
		count = 1
		result.Truncated = true
	}
	content, err := em.ReadContent(&row, &count)
	if err != nil {
		return result, fmt.Errorf("read execution content: %w", err)
	}
	result.Lines = content.Lines
	result.Text = content.Text
	if len(result.Text) > opts.MaxBytes {
		b := []byte(result.Text[:opts.MaxBytes])
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
		result.Text = string(b)
		result.Lines = strings.Split(result.Text, "\n")
		result.Truncated = true
	}
	return result, executionErr
}

func (t *Terminal) interactivePOSIXShell() bool {
	if t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	switch filepath.Base(t.cmd.Path) {
	case "sh", "bash", "dash", "ksh", "zsh":
	default:
		return false
	}
	for _, arg := range t.cmd.Args[1:] {
		if arg == "--" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			return false
		}
		if strings.HasPrefix(arg, "--") {
			switch arg {
			case "--norc", "--noprofile", "--posix", "--login", "--interactive":
				continue
			default:
				return false
			}
		}
		if strings.Contains(strings.TrimPrefix(arg, "-"), "c") {
			return false
		}
	}
	return true
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

// Canonical PTY line limits also apply inside quotes. Transport long input
// through short quoted assignments, then evaluate the assembled command.
func splitExecuteInput(line string) string {
	line = strings.TrimSuffix(line, "\n")

	// Build the complete command as data, then evaluate it once. Each assignment
	// is independently below Darwin's canonical line limit, including quote escapes.
	var b strings.Builder
	b.WriteString("water_exec_input=''\n")
	for len(line) > 0 {
		n := min(128, len(line))
		b.WriteString("water_exec_input=$water_exec_input")
		b.WriteString(shellQuote(line[:n]))
		b.WriteByte('\n')
		line = line[n:]
	}
	b.WriteString("eval \"$water_exec_input\"; unset water_exec_input\n")
	return b.String()
}

type executeMarkerParser struct {
	prefix, pending []byte
	normal          func([]byte)
	marker          func(string)
}

func (p *executeMarkerParser) write(data []byte) {
	p.pending = append(p.pending, data...)
	for len(p.pending) > 0 {
		i := bytes.Index(p.pending, p.prefix)
		if i < 0 {
			n := len(p.pending)
			for k := min(n, len(p.prefix)-1); k > 0; k-- {
				if bytes.Equal(p.pending[n-k:], p.prefix[:k]) {
					n -= k
					break
				}
			}
			p.normal(p.pending[:n])
			p.pending = p.pending[n:]
			return
		}
		if i > 0 {
			p.normal(p.pending[:i])
			p.pending = p.pending[i:]
		}
		j := bytes.IndexByte(p.pending[len(p.prefix):], 7)
		if j < 0 {
			if len(p.pending) > len(p.prefix)+64 {
				p.normal(p.pending[:1])
				p.pending = p.pending[1:]
				continue
			}
			return
		}
		j += len(p.prefix)
		p.marker(string(p.pending[len(p.prefix):j]))
		p.pending = p.pending[j+1:]
	}
}
func (p *executeMarkerParser) flush() {
	if len(p.pending) > 0 {
		p.normal(p.pending)
		p.pending = nil
	}
}

// PTYs are made nonblocking and registered with the runtime poller at spawn.
// A write deadline bounds queue pressure without spawning an uncancellable writer.
func (t *Terminal) executeWrite(data []byte) error {
	if err := t.ptmx.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
		return err
	}
	defer t.ptmx.SetWriteDeadline(time.Time{})
	return t.Write(data)
}

var executeRangeEscape = regexp.MustCompile(`\x1b(?:\[[0-9;?]*[AHfduJSTr]|[78DM])`)
var executeAltEscape = regexp.MustCompile(`\x1b\[\?(?:47|1047|1049)[hl]`)
