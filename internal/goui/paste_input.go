package goui

import (
	"bytes"
	"errors"
	"fmt"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/google/uuid"
)

// Darwin and other Unix PTYs bound cooked input with MAX_CANON/MAX_INPUT (1024).
// A single ordinary paste larger than that is truncated or discarded for programs
// like cat that keep ICANON. Bracketed paste usually accompanies raw mode, so it
// can travel in larger session chunks. Keep ordinary paste under the kernel cap
// and leave a short gap for the reader to drain between chunks.
const (
	pasteCanonChunk     = 512
	pasteSessionChunk   = 64 * 1024
	pastePaceInterval   = 5 * time.Millisecond
	bracketedPasteStart = "\x1b[200~"
)

func isBracketedPastePayload(data []byte) bool {
	return bytes.HasPrefix(data, []byte(bracketedPasteStart))
}

// nextPasteChunk returns the next write for paced ordinary paste. Prefer a
// newline boundary so each cooked line stays independently deliverable.
func nextPasteChunk(data []byte, max int) (chunk, rest []byte) {
	if max <= 0 || len(data) <= max {
		return data, nil
	}
	if i := bytes.LastIndexByte(data[:max], '\n'); i >= 0 {
		return data[:i+1], data[i+1:]
	}
	return data[:max], data[max:]
}

func (c *WorkspaceClient) deliverTerminalInput(term *terminalClient, id uuid.UUID, data []byte) {
	if len(data) == 0 {
		return
	}
	term.inputPending.Store(true)
	term.noteInputEcho(time.Now())
	if len(data) <= 64 {
		c.echoWaitTerm.Store(term)
		countNativeWork("count.echo_wait_marked", 1)
	}
	select {
	case c.snapshotWake <- struct{}{}:
	default:
	}
	if c.invalidate != nil {
		c.invalidate()
	}

	// Keep FIFO order: anything already queued must drain before a new write, and
	// large ordinary pastes must stay under the canonical input limit. Bracketed
	// pastes and tiny keystrokes keep the direct path when the queue is idle.
	term.mu.Lock()
	hasPending := len(term.pendingInput) > 0 || term.inputPumping
	term.mu.Unlock()
	if hasPending || (!isBracketedPastePayload(data) && len(data) > pasteCanonChunk) {
		c.queueTerminalInput(term, id, data)
		return
	}
	c.writeTerminalInput(term, id, data, pasteSessionChunk)
}

func (c *WorkspaceClient) queueTerminalInput(term *terminalClient, id uuid.UUID, data []byte) {
	term.mu.Lock()
	term.pendingInput = append(term.pendingInput, data...)
	start := !term.inputPumping
	term.inputPumping = true
	term.mu.Unlock()
	if start {
		go c.pumpTerminalInput(term, id)
	}
}

func (c *WorkspaceClient) writeTerminalInput(term *terminalClient, id uuid.UUID, data []byte, chunkSize int) {
	end := traceNativeWork("input.send")
	defer func() {
		if end != nil {
			end()
		}
	}()
	for len(data) > 0 {
		if c.stopped.Load() || term.closed.Load() {
			return
		}
		chunk := data
		if chunkSize > 0 && len(chunk) > chunkSize {
			chunk = chunk[:chunkSize]
		}
		err := c.session.SendInput(id, chunk)
		if err == nil {
			data = data[len(chunk):]
			continue
		}
		if errors.Is(err, goclient.ErrSessionBackpressure) {
			c.queueTerminalInput(term, id, data)
			term.mu.Lock()
			term.inputError = "Input backpressured; retry"
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
			return
		}
		term.mu.Lock()
		term.inputError = fmt.Sprintf("terminal %s input stopped: %v", term.id, err)
		term.mu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
		return
	}
	term.mu.Lock()
	if term.inputError == "Input backpressured; retry" {
		term.inputError = ""
	}
	term.mu.Unlock()
}

func (c *WorkspaceClient) pumpTerminalInput(term *terminalClient, id uuid.UUID) {
	defer func() {
		term.mu.Lock()
		term.inputPumping = len(term.pendingInput) > 0
		restart := term.inputPumping
		term.mu.Unlock()
		if restart && !c.stopped.Load() && !term.closed.Load() {
			go c.pumpTerminalInput(term, id)
		}
	}()
	for {
		if c.stopped.Load() || term.closed.Load() {
			term.mu.Lock()
			term.pendingInput = nil
			term.inputPumping = false
			term.mu.Unlock()
			return
		}
		term.mu.Lock()
		if len(term.pendingInput) == 0 {
			term.inputPumping = false
			term.mu.Unlock()
			return
		}
		bracketed := isBracketedPastePayload(term.pendingInput)
		max := pasteCanonChunk
		if bracketed {
			max = pasteSessionChunk
		}
		chunk, rest := nextPasteChunk(term.pendingInput, max)
		term.pendingInput = append([]byte(nil), rest...)
		term.mu.Unlock()

		end := traceNativeWork("input.send")
		err := c.session.SendInput(id, chunk)
		if end != nil {
			end()
		}
		if err == nil {
			term.mu.Lock()
			if term.inputError == "Input backpressured; retry" {
				term.inputError = ""
			}
			term.mu.Unlock()
			if !bracketed {
				time.Sleep(pastePaceInterval)
			}
			continue
		}
		if errors.Is(err, goclient.ErrSessionBackpressure) {
			term.mu.Lock()
			term.pendingInput = append(append([]byte(nil), chunk...), term.pendingInput...)
			term.inputError = "Input backpressured; retry"
			term.mu.Unlock()
			if c.invalidate != nil {
				c.invalidate()
			}
			time.Sleep(pastePaceInterval)
			continue
		}
		term.mu.Lock()
		term.pendingInput = nil
		term.inputPumping = false
		term.inputError = fmt.Sprintf("terminal %s input stopped: %v", term.id, err)
		term.mu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
		return
	}
}
