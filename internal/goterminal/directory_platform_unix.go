//go:build !windows

package goterminal

func (t *Terminal) platformDirectory() (string, bool) { return "", false }
