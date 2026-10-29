// Package goplatform contains the small OS boundaries shared by Water processes.
package goplatform

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// ExecutableName returns the packaged filename for a product binary.
func ExecutableName(name string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name + ".exe"
	}
	return name
}

// RuntimeDirectory keeps Windows endpoints in the current user's private profile.
// Unix paths remain short enough for sockaddr_un and retain existing discovery.
func RuntimeDirectory() string {
	if runtime.GOOS == "windows" {
		if dir, err := os.UserCacheDir(); err == nil {
			return filepath.Join(dir, "Water", "run")
		}
		return filepath.Join(os.TempDir(), "Water-run")
	}
	return "/tmp"
}

func DefaultSocket(variant string) string {
	name := "water.sock"
	if variant != "release" {
		name = "water-dev.sock"
	}
	return filepath.Join(RuntimeDirectory(), name)
}

// UserToken separates per-user endpoints without requiring Unix numeric IDs.
func UserToken() string {
	if runtime.GOOS != "windows" {
		return fmt.Sprint(os.Geteuid())
	}
	identity := os.Getenv("USERPROFILE")
	if current, err := user.Current(); err == nil {
		identity = current.Uid
	}
	sum := sha256.Sum256([]byte(identity))
	return fmt.Sprintf("%x", sum[:4])
}
