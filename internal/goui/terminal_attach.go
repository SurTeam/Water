package goui

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
)

// An attempt owns setup cancellation. After installation the same context
// belongs to the terminal subscription, so canceling this attempt can never
// detach another attempt for the same terminal UUID.
type terminalAttachAttempt struct {
	cancelled atomic.Bool
	ctx       context.Context
	cancel    context.CancelFunc
}

func newTerminalAttachAttempt() *terminalAttachAttempt {
	ctx, cancel := context.WithCancel(context.Background())
	return &terminalAttachAttempt{ctx: ctx, cancel: cancel}
}
func (a *terminalAttachAttempt) Load() bool { return a.cancelled.Load() }
func (a *terminalAttachAttempt) Store(value bool) {
	a.cancelled.Store(value)
	if value {
		a.cancel()
	}
}

func retryTerminalAttach(ctx context.Context, attach func() (*goclient.TerminalSubscription, error)) (*goclient.TerminalSubscription, error) {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sub, err := attach()
		if err == nil {
			return sub, nil
		}
		if attempt == 3 {
			return nil, err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
