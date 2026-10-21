package goui

import (
	"errors"
	"time"
)

// PromptSSH marshals OpenSSH askpass requests onto the existing remote modal.
// The reply is held only for the lifetime of this authentication attempt.
func (c *WorkspaceClient) PromptSSH(prompt string) (string, error) {
	reply := make(chan *string, 1)
	c.connectionMu.Lock()
	c.remoteAuthPrompt = prompt
	c.remoteAuthReply = reply
	c.remoteFormVisible = true
	c.connectionMu.Unlock()
	if c.invalidate != nil {
		c.invalidate()
	}
	defer func() {
		c.layoutMu.Lock()
		defer c.layoutMu.Unlock()
		c.connectionMu.Lock()
		if c.remoteAuthReply == reply {
			c.remoteAuthReply = nil
			c.remoteAuthPrompt = ""
			c.remoteAuthEditor.SetText("")
		}
		c.connectionMu.Unlock()
	}()
	select {
	case value := <-reply:
		if value == nil {
			return "", errors.New("authentication canceled")
		}
		return *value, nil
	case <-time.After(170 * time.Second):
		return "", errors.New("authentication timed out")
	}
}

func (c *WorkspaceClient) ShowSSHWarnings(message string) {
	if message == "" {
		return
	}
	c.connectionMu.Lock()
	c.remoteError = message
	c.remoteFormVisible = true
	c.connectionMu.Unlock()
}
