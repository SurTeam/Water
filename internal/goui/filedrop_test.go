package goui

import (
	"io/fs"
	"testing"
	"time"
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

type stubAbsEntry struct {
	name string
	path string
	dir  bool
}

func (e stubAbsEntry) Name() string { return e.name }
func (e stubAbsEntry) IsDir() bool  { return e.dir }
func (e stubAbsEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e stubAbsEntry) Info() (fs.FileInfo, error) { return stubAbsInfo{e}, nil }
func (e stubAbsEntry) AbsPath() string            { return e.path }

type stubAbsInfo struct{ stubAbsEntry }

func (stubAbsInfo) Size() int64        { return 0 }
func (stubAbsInfo) Mode() fs.FileMode  { return 0 }
func (stubAbsInfo) ModTime() time.Time { return time.Time{} }
func (stubAbsInfo) Sys() any           { return nil }

type stubPlainEntry struct {
	name string
	dir  bool
}

func (e stubPlainEntry) Name() string               { return e.name }
func (e stubPlainEntry) IsDir() bool                { return e.dir }
func (e stubPlainEntry) Type() fs.FileMode          { return 0 }
func (e stubPlainEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrInvalid }

type stubDropFS map[string][]fs.DirEntry

func (s stubDropFS) Open(string) (fs.File, error) { return nil, fs.ErrInvalid }
func (s stubDropFS) ReadDir(name string) ([]fs.DirEntry, error) {
	ents, ok := s[name]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return ents, nil
}

func TestPathsFromDroppedFS(t *testing.T) {
	// Dropped directory is a leaf: its real children do not implement AbsPath.
	fsys := stubDropFS{
		".": {
			stubAbsEntry{name: "mid", path: "/tmp/mid", dir: true},
			stubAbsEntry{name: "notes", path: "/tmp/notes", dir: true},
		},
		"mid": {
			stubAbsEntry{name: "a.txt", path: "/tmp/mid/a.txt"},
		},
		"notes": {
			stubPlainEntry{name: "inside.txt"},
		},
	}
	got := pathsFromDroppedFS(fsys)
	want := []string{"/tmp/mid/a.txt", "/tmp/notes"}
	if len(got) != len(want) {
		t.Fatalf("got %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}
