package goui

import (
	"io/fs"
	"net/url"
	"path/filepath"
	"runtime"
	"strings"
)

// shellQuote wraps a file path in single quotes for safe shell insertion.
// Embedded single quotes are escaped as '\” (end quote, escaped quote, restart quote).
func shellQuote(path string) string {
	return "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
}

// fileDropToText converts a list of file paths to a shell-safe quoted string
// suitable for insertion into a terminal prompt.
func fileDropToText(paths []string) string {
	quoted := make([]string, len(paths))
	for i, p := range paths {
		quoted[i] = shellQuote(p)
	}
	return strings.Join(quoted, " ")
}

// clipboardImagePath identifies a single image file reference, including the
// plain-text path representation supplied by some image applications.
func clipboardImagePath(value string) bool {
	value = strings.TrimSpace(value)
	if u, err := url.Parse(value); err == nil && u.Scheme == "file" {
		value = u.Path
		if runtime.GOOS == "windows" {
			if u.Host != "" && u.Host != "localhost" {
				value = `\\` + u.Host + filepath.FromSlash(u.Path)
			} else {
				if len(value) >= 3 && value[0] == '/' && value[2] == ':' {
					value = value[1:]
				}
				value = filepath.FromSlash(value)
			}
		}
	}
	if !filepath.IsAbs(value) || strings.ContainsAny(value, "\t\r\n") {
		return false
	}
	switch strings.ToLower(filepath.Ext(value)) {
	case ".png", ".tif", ".tiff", ".jpg", ".jpeg", ".heic", ".heif", ".gif", ".bmp", ".webp":
		return true
	}
	return false
}

// absPather is implemented by desktop drop entries that know their real path.
type absPather interface {
	AbsPath() string
}

// pathsFromDroppedFS returns the absolute paths of files and directories the
// user dropped. Intermediate directories that only exist so the virtual FS can
// reach those items are skipped; a dropped directory is not expanded.
func pathsFromDroppedFS(fsys fs.FS) []string {
	if fsys == nil {
		return nil
	}
	var out []string
	var walk func(name string)
	walk = func(name string) {
		ents, err := fs.ReadDir(fsys, name)
		if err != nil {
			return
		}
		for _, ent := range ents {
			child := ent.Name()
			if name != "." {
				child = name + "/" + ent.Name()
			}
			if ent.IsDir() && droppedFSHasNestedPaths(fsys, child) {
				walk(child)
				continue
			}
			if p, ok := ent.(absPather); ok && p.AbsPath() != "" {
				out = append(out, p.AbsPath())
			}
		}
	}
	walk(".")
	return out
}

func droppedFSHasNestedPaths(fsys fs.FS, name string) bool {
	ents, err := fs.ReadDir(fsys, name)
	if err != nil {
		return false
	}
	for _, ent := range ents {
		if _, ok := ent.(absPather); ok {
			return true
		}
	}
	return false
}

// fileDropText uses the target connection's shell dialect. SSH targets retain
// POSIX quoting even when the desktop itself runs on Windows.
func (c *WorkspaceClient) fileDropText(paths []string) string {
	if runtime.GOOS != "windows" || c.remoteDestination != "" {
		return fileDropToText(paths)
	}
	return windowsFileDropToText(paths, c.currentConfig().Shell.Program)
}

func windowsFileDropToText(paths []string, program string) string {
	name := strings.ToLower(filepath.Base(program))
	quoted := make([]string, len(paths))
	for i, path := range paths {
		switch name {
		case "cmd", "cmd.exe":
			// cmd paths use double quotes; percent expansion follows cmd semantics.
			quoted[i] = `"` + strings.ReplaceAll(path, `"`, `""`) + `"`
		case "sh", "sh.exe", "bash", "bash.exe", "zsh", "zsh.exe":
			quoted[i] = shellQuote(path)
		default:
			quoted[i] = "'" + strings.ReplaceAll(path, "'", "''") + "'"
		}
	}
	return strings.Join(quoted, " ")
}
