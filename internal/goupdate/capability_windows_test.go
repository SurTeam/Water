package goupdate

import "testing"

func TestWindowsUpdaterCapability(t *testing.T) {
	manager := NewManager("0.4.0", "dev", nil, nil)
	defer manager.Close()
	if state := manager.Snapshot(); state.Phase != "unsupported" || state.Error == "" {
		t.Fatalf("unsupported platform not reported: %#v", state)
	}
	manager.Check()
	if state := manager.Snapshot(); state.Phase != "unsupported" {
		t.Fatalf("check changed unsupported state: %#v", state)
	}
	if err := StartInstaller(nil); err == nil {
		t.Fatal("installer accepted Windows plan")
	}
	if err := RunInstaller("missing.json"); err == nil {
		t.Fatal("internal installer accepted Windows invocation")
	}
}
