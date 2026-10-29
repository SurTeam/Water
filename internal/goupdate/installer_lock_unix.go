//go:build !windows

package goupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

func processAlive(pid int) bool {
	if pid <= 1 {
		return true
	}
	err := syscall.Kill(pid, 0)
	return err != syscall.ESRCH
}
func acquireGuard(path string) (*os.File, error) {
	f, err := os.OpenFile(path+".guard", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
func inheritedGuard() (*os.File, error) {
	f := os.NewFile(3, "update guard")
	if f == nil {
		return nil, fmt.Errorf("missing guard")
	}
	if _, err := f.Stat(); err != nil {
		return nil, err
	}
	syscall.CloseOnExec(3)
	return f, nil
}
func recoverVisible(plan *Plan) error {
	path := lockPath(plan)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var owner struct{ PID int }
	if len(data) > 0 {
		if json.Unmarshal(data, &owner) != nil || owner.PID <= 1 {
			return fmt.Errorf("unrecognized update lock")
		}
		if processAlive(owner.PID) {
			return fmt.Errorf("installer is active")
		}
	} else {
		stages, err := filepath.Glob(filepath.Join(filepath.Dir(plan.Directory), "."+filepath.Base(plan.Executable)+"-update-*"))
		if err != nil {
			return err
		}
		for _, stage := range stages {
			info, err := os.Stat(stage)
			if err != nil {
				return err
			}
			if !info.IsDir() {
				continue
			}
			b, err := os.ReadFile(filepath.Join(stage, "installer.pid"))
			if os.IsNotExist(err) {
				// A legacy helper may not yet have written its PID. Preserve uncertain staging.
				if _, e := os.Stat(filepath.Join(stage, "plan.json")); e == nil {
					return fmt.Errorf("legacy installer ownership is uncertain")
				}
				continue
			}
			if err != nil {
				return err
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 1 {
				return fmt.Errorf("legacy installer ownership is uncertain")
			}
			if processAlive(pid) {
				return fmt.Errorf("legacy installer is active")
			}
		}
	}
	return os.Remove(path)
}
func recoverStale(executable string) {
	plan := &Plan{Executable: executable, Directory: filepath.Join(filepath.Dir(executable), "unused")}
	// Bundle staging lives beside the bundle, rather than beside Contents/MacOS.
	if i := strings.Index(executable, ".app/Contents/MacOS/"); i >= 0 {
		plan.Directory = filepath.Join(filepath.Dir(executable[:i+4]), "unused")
	}
	f, err := acquireGuard(lockPath(plan))
	if err != nil {
		return
	}
	defer f.Close()
	_ = recoverVisible(plan)
}
