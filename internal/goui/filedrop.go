package goui

import (
	"io/fs"
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
