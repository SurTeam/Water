//go:build !windows

package goterminal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestCurrentDirectoryChineseUnderCLocale(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	directory := filepath.Join(t.TempDir(), "进度", "paper with spaces")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	// macOS resolves /var to /private/var when reporting a process cwd.
	directory, _ = filepath.EvalSymlinks(directory)
	registry := NewRegistry()
	t.Cleanup(registry.CloseAll)
	term, err := registry.SpawnWithDir("/bin/sh", []string{"-i"}, goprotocol.TerminalSize{Columns: 80, Lines: 24}, directory)
	if err != nil {
		t.Fatal(err)
	}
	waitExecutionShell(t, term)
	if got := term.CurrentDirectory(); got != directory {
		t.Fatalf("cwd = %q, want %q", got, directory)
	}
}
