package goui

import (
	"github.com/SurTeam/Water/internal/goupdate"
	"testing"
)

func TestQuitAndServerBlockedDuringInstall(t *testing.T) {
	w := &EbitengineWindow{}
	called := make(chan struct{}, 1)
	w.SetQuitServerHandler(func() error { called <- struct{}{}; return nil })
	w.updating.Store(true)
	w.menuAction("quit-and-server")
	if w.quitResult != nil {
		t.Fatal("shutdown started during installation")
	}
	select {
	case <-called:
		t.Fatal("shutdown callback ran")
	default:
	}
}

func TestInstallBlockedDuringQuitAndServer(t *testing.T) {
	w := &EbitengineWindow{quitResult: make(chan error, 1)}
	// An unprepared manager would fail immediately if Install were reached.
	w.updates = goupdate.NewManager("0.0.0", "dev", nil, func() {})
	defer w.updates.Close()
	w.SetUpdateRestartAllowed(true)
	w.updateAction("install")
	if w.Updating() || w.updateResult != nil {
		t.Fatal("installation started while server shutdown was pending")
	}
}
