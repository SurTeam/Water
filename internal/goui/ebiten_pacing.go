package goui

import (
	"time"

	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
	"github.com/hajimehoshi/ebiten/v2"
)

// A fast byte stream does not require presenting every intermediate screen.
// Keep this clock independent of display-rate pointer/keyboard interaction.
//
// Non-echo streams never publish faster than 60/s, but must not be slower than
// the display either: on a 30Hz panel a fixed 60Hz window would queue a second
// frame that can never be shown. The input echo has its own bounded bypass.
func publicationPeriod(frameInterval time.Duration) time.Duration {
	period := time.Second / 60
	if frameInterval > period {
		period = frameInterval
	}
	return min(period, time.Second/10)
}

// echoBypassRate caps how many input echoes may publish ahead of the shared
// window per connection. One per frame is not a bound on a 120/144Hz display,
// so a token bucket keeps the bypass at 60/s while the window still serves
// every non-echo stream.
const echoBypassRate = 60

// tokenBucket is a bounded-rate gate with capacity one. It is owned by the
// pump goroutine, so it needs no locking.
type tokenBucket struct {
	tokens float64
	last   time.Time
}

func (b *tokenBucket) take(now time.Time) bool {
	if b.last.IsZero() {
		// Capacity one, starting full: a single burst is allowed, then the
		// sustained rate is echoBypassRate.
		b.tokens = 1
	} else {
		b.tokens = min(1, b.tokens+now.Sub(b.last).Seconds()*echoBypassRate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func outputPublicationDelay(published, now time.Time, frameInterval time.Duration) time.Duration {
	if published.IsZero() {
		return 0
	}
	return max(0, publicationPeriod(frameInterval)-now.Sub(published))
}

// noteVisibleTerminals publishes the terminals this window is actually drawing.
// Output for anything else must not schedule a frame.
func (w *EbitengineWindow) noteVisibleTerminals(c *WorkspaceClient) {
	ids := make(map[uuid.UUID]struct{})
	if v := w.views[c]; v != nil {
		for _, pane := range v.panes {
			if pane.term != nil {
				ids[pane.term.id] = struct{}{}
			}
		}
	}
	w.visibleTerminals.Store(&ids)
}

// Keep idle maintenance independent of display cadence. A visible blinking
// cursor adds only its next phase deadline (600ms on, 400ms off).
func frameScheduleDelay(now time.Time, pending, blinking bool) time.Duration {
	if pending {
		return time.Second / 60
	}
	delay := 250 * time.Millisecond
	if blinking {
		phase := time.Duration(now.UnixNano() % int64(time.Second))
		next := time.Second
		if phase < 600*time.Millisecond {
			next = 600 * time.Millisecond
		}
		delay = min(delay, next-phase)
	}
	return delay
}

func terminalCursorVisible(snap govt.Snapshot) bool {
	return !snap.CursorHide && snap.CursorX >= 0 && snap.CursorX < snap.Cols && snap.CursorY >= 0 && snap.CursorY < snap.Rows
}

func (w *EbitengineWindow) focusedCursorBlinks(c *WorkspaceClient) bool {
	if c == nil || !ebiten.IsFocused() || !windowChromeVisible(w) {
		return false
	}
	v := w.views[c]
	if v == nil {
		return false
	}
	id, ok := c.activeTerminalID()
	if !ok {
		return false
	}
	for _, pane := range v.panes {
		if pane.term == nil || pane.term.id != id {
			continue
		}
		pane.term.mu.RLock()
		blink := pane.term.snapshot.CursorBlink && terminalCursorVisible(pane.term.snapshot)
		pane.term.mu.RUnlock()
		return blink
	}
	return false
}

func ensureTerminalView(term *terminalClient) {
	if term == nil || !term.viewStale.Load() {
		return
	}
	// A hidden terminal may still be parsing a large output event. Keep the
	// last immutable view and retry next frame instead of blocking the GUI.
	if !term.parseMu.TryLock() {
		return
	}
	defer term.parseMu.Unlock()
	term.mu.Lock()
	defer term.mu.Unlock()
	if term.emu == nil || !term.viewStale.Swap(false) {
		return
	}
	term.snapshot = term.emu.FrameSnapshot()
}

// Active input/output follows display presentation rather than two free-running
// 60Hz clocks. Idle/occluded windows sleep and retain their last rendered frame.
func (w *EbitengineWindow) updateFramePacing() {
	visible := windowChromeVisible(w)
	w.revealHold = nextRevealHold(w.revealHold, w.lastPacingVisible, visible)
	w.lastPacingVisible = visible
	active := w.continuousInput.Load() || len(w.queue) > 0 || time.Since(time.Unix(0, w.frameActivity.Load())) < 120*time.Millisecond
	if w.continuousInput.Load() {
		countNativeWork("count.continuous_input", 1)
	}
	// Enabling vsync in the same frame the window becomes visible makes Metal
	// wait on a display link that cannot produce a drawable until this thread
	// returns to the run loop. Keep event-paced frames until the reveal settles.
	mode := ebiten.FPSModeVsyncOffMinimum
	if visible && w.revealHold == 0 && active && !w.catchingUp {
		mode = ebiten.FPSModeVsyncOn
	}
	if ebiten.FPSMode() != mode {
		ebiten.SetFPSMode(mode)
	}
}

func windowChromeVisible(w *EbitengineWindow) bool {
	visible := !ebiten.IsWindowMinimized()
	if platform, ok := w.platform.(interface{ chromeVisible() bool }); ok {
		visible = visible && platform.chromeVisible()
	} else if w.platform != nil {
		state := w.platform.Snapshot()
		visible = visible && state["hidden"] != true && state["occluded"] != true
	}
	return visible
}

// nextRevealHold counts down frames after the window becomes visible again.
// A positive value keeps presentation off the display link.
func nextRevealHold(hold int, wasVisible, visible bool) int {
	if !visible {
		return hold
	}
	if !wasVisible {
		return 8
	}
	if hold > 0 {
		return hold - 1
	}
	return 0
}

type nativeKeyRepeater struct {
	next     map[ebiten.Key]time.Time
	consumed map[ebiten.Key]bool
	initial  time.Duration
	repeat   time.Duration
}

func (r *nativeKeyRepeater) useSystemRepeat(initial, interval time.Duration) {
	if r.initial == 0 && initial > 0 {
		r.initial = initial
	}
	if r.repeat == 0 && interval > 0 {
		r.repeat = interval
	}
}

// Observe releases even on ticks owned by the IME. A consumed press must not
// become a new physical press on the following tick while it is still held.
func (r *nativeKeyRepeater) observe(pressed func(ebiten.Key) bool, consume bool) {
	for k := ebiten.Key(0); k <= ebiten.KeyMax; k++ {
		if !pressed(k) {
			delete(r.next, k)
			delete(r.consumed, k)
		} else if consume {
			if r.consumed == nil {
				r.consumed = make(map[ebiten.Key]bool)
			}
			r.consumed[k] = true
			delete(r.next, k)
		}
	}
}

// Physical navigation/control keys repeat by elapsed time, independent of a
// 60Hz/120Hz display. Never replay missed repeats as a burst after a slow frame.
func (r *nativeKeyRepeater) ready(k ebiten.Key, pressed bool, now time.Time) bool {
	if !pressed {
		delete(r.next, k)
		delete(r.consumed, k)
		return false
	}
	if r.consumed[k] {
		return false
	}
	if r.next == nil {
		r.next = make(map[ebiten.Key]time.Time)
	}
	next, held := r.next[k]
	initial, interval := 350*time.Millisecond, 50*time.Millisecond
	if r.initial > 0 {
		initial = r.initial
	}
	if r.repeat > 0 {
		interval = r.repeat
	}
	if !held {
		r.next[k] = now.Add(initial)
		return true
	}
	if now.Before(next) {
		return false
	}
	r.next[k] = now.Add(interval)
	return true
}
