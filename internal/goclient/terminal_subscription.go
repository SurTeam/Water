package goclient

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

// TerminalSubscription owns one independent connection. Events is installed
// before terminal.attach is sent; consuming it directly preserves the boundary
// between the attach replay and live output without an aggregate forwarding queue.
// A full channel backpressures only this connection. Close affects only this
// subscription, including when another subscription attaches the same UUID.
type TerminalSubscription struct {
	Events  <-chan TerminalPush
	parent  *Session
	session *Session
	done    chan struct{}
	once    sync.Once
	cancel  context.CancelFunc
}

func (s *TerminalSubscription) Close() error {
	var err error
	s.once.Do(func() {
		close(s.done)
		s.cancel()
		s.parent.terminalMu.Lock()
		delete(s.parent.terminalSubscriptions, s)
		s.parent.terminalMu.Unlock()
		err = s.session.Close()
		// Cancellation discards this subscription's pending live payloads and
		// joins its reader without involving any emulator/parser lock.
		for range s.Events {
		}
	})
	return err
}

func (s *TerminalSubscription) Err() error { return s.session.Err() }

// AttachTerminal creates an owned subscription rather than replacing the
// legacy per-UUID aggregate stream. The context owns setup and the subscription
// lifetime; canceling a stale attach cannot detach a newer connection.
func (s *Session) AttachTerminal(ctx context.Context, id uuid.UUID, out any) (*TerminalSubscription, error) {
	if s.SocketPath == "" {
		return nil, errors.New("isolated terminal subscriptions require a socket transport")
	}
	ctx, cancel := context.WithCancel(ctx)
	transferred := false
	defer func() {
		if !transferred {
			cancel()
		}
	}()
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	client := New(s.SocketPath)
	client.Build = s.Build
	stream, err := client.openSessionContext(ctx, "terminal")
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	sub := &TerminalSubscription{Events: stream.Events, parent: s, session: stream, done: make(chan struct{}), cancel: cancel}
	s.terminalMu.Lock()
	select {
	case <-s.done:
		s.terminalMu.Unlock()
		_ = stream.Close()
		return nil, errors.New("session closed")
	default:
	}
	if s.terminalSubscriptions == nil {
		s.terminalSubscriptions = make(map[*TerminalSubscription]struct{})
	}
	s.terminalSubscriptions[sub] = struct{}{}
	s.terminalMu.Unlock()
	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Close()
		case <-sub.done:
		}
	}()
	if err := stream.Call("terminal.attach", map[string]any{"terminal_id": id}, out); err != nil {
		_ = sub.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	if ctx.Err() != nil {
		_ = sub.Close()
		return nil, ctx.Err()
	}
	transferred = true
	return sub, nil
}
