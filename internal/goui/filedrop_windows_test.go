package goui

import "testing"

func TestWindowsFileDropQuoting(t *testing.T) {
	paths := []string{`C:\my files\it's.txt`, `C:\other\ok.txt`}
	if got := windowsFileDropToText(paths, "powershell.exe"); got != `'C:\my files\it''s.txt' 'C:\other\ok.txt'` {
		t.Fatalf("PowerShell: %q", got)
	}
	if got := windowsFileDropToText(paths, "cmd.exe"); got != `"C:\my files\it's.txt" "C:\other\ok.txt"` {
		t.Fatalf("cmd: %q", got)
	}
	if !clipboardImagePath("file:///C:/my%20files/image.png") {
		t.Fatal("Windows file URI not recognized")
	}
	if !clipboardImagePath("file://server/share/image.png") {
		t.Fatal("UNC file URI not recognized")
	}
}
