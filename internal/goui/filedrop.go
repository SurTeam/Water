package goui

import (
	"strings"
)

// shellQuote wraps a file path in single quotes for safe shell insertion.
// Embedded single quotes are escaped as '\'' (end quote, escaped quote, restart quote).
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