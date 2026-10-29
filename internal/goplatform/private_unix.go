//go:build !windows

package goplatform

import "os"

func MkdirPrivate(path string) error               { return os.MkdirAll(path, 0700) }
func IsPrivate(path string, info os.FileInfo) bool { return info.Mode().Perm()&0077 == 0 }
