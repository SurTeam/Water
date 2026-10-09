package goui

import (
	"testing"
)

func TestShellQuote(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"/tmp/file.txt", "'/tmp/file.txt'"},
		{"/tmp/my file.txt", "'/tmp/my file.txt'"},
		{`/tmp/it's.txt`, `'/tmp/it'\''s.txt'`},
		{`/tmp/a'b c.txt`, `'/tmp/a'\''b c.txt'`},
		{"/tmp/normal", "'/tmp/normal'"},
		{"", "''"},
	} {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFileDropToText(t *testing.T) {
	// Single file
	got := fileDropToText([]string{"/tmp/a.txt"})
	if got != "'/tmp/a.txt'" {
		t.Errorf("single file: got %q", got)
	}
	// Multiple files
	got = fileDropToText([]string{"/tmp/a.txt", "/tmp/b file.txt"})
	if got != "'/tmp/a.txt' '/tmp/b file.txt'" {
		t.Errorf("multiple files: got %q", got)
	}
	// Empty
	got = fileDropToText(nil)
	if got != "" {
		t.Errorf("empty: got %q", got)
	}
}