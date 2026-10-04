package goupdate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

const InstallerArgument = "--internal-install-update"

func lockPath(plan *Plan) string {
	return filepath.Join(filepath.Dir(plan.Directory), "."+filepath.Base(plan.Executable)+"-update-lock")
}

// StartInstaller transfers ownership of the staging directory to a separate
// process. Installation starts only after the GUI has exited normally.
func StartInstaller(plan *Plan) error {
	lock := lockPath(plan)
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("another update is installing, or the update lock needs cleanup: %w", err)
	}
	f.Close()
	started := false
	defer func() {
		if !started {
			os.Remove(lock)
		}
	}()
	helper := filepath.Join(plan.Directory, filepath.Base(plan.Helper))
	if err = copyExecutable(plan.Helper, helper); err != nil {
		return err
	}
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	path := filepath.Join(plan.Directory, "plan.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(plan.Directory, "install.log"), os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(helper, InstallerArgument, path)
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	started = true
	go func() { _ = cmd.Wait() }()
	return nil
}

func copyExecutable(source, destination string) error {
	src, err := os.Open(source)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// replace is a transaction: every old entry is retained until the new GUI
// acknowledges a live frame. Any replacement or startup error rolls back all
// entries, including the separately packaged Linux server.
func replace(plan *Plan, launch func() error) error {
	type movedEntry struct {
		entry     Entry
		backup    string
		installed bool
	}
	var moved []movedEntry
	rollback := func() error {
		var failure error
		for i := len(moved) - 1; i >= 0; i-- {
			m := moved[i]
			if m.installed {
				if err := os.Rename(m.entry.Target, m.entry.Source); err != nil {
					failure = err
					continue
				}
			}
			if err := os.Rename(m.backup, m.entry.Target); err != nil {
				failure = err
			}
		}
		return failure
	}
	for i, e := range plan.Entries {
		backup := filepath.Join(plan.Directory, fmt.Sprintf("backup-%d", i))
		if err := os.Rename(e.Target, backup); err != nil {
			if recovery := rollback(); recovery != nil {
				return fmt.Errorf("replacement failed: %v; rollback failed: %w", err, recovery)
			}
			return err
		}
		moved = append(moved, movedEntry{entry: e, backup: backup})
		if err := os.Rename(e.Source, e.Target); err != nil {
			if recovery := rollback(); recovery != nil {
				return fmt.Errorf("replacement failed: %v; rollback failed: %w", err, recovery)
			}
			return err
		}
		moved[len(moved)-1].installed = true
	}
	if err := launch(); err != nil {
		if recovery := rollback(); recovery != nil {
			return fmt.Errorf("startup failed: %v; rollback failed: %w", err, recovery)
		}
		return err
	}
	return nil
}

func RunInstaller(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var plan Plan
	if err = json.Unmarshal(data, &plan); err != nil {
		return err
	}
	if plan.ParentPID <= 1 || len(plan.Entries) == 0 || len(plan.Entries) > 3 || filepath.Dir(path) != plan.Directory {
		return fmt.Errorf("invalid installer plan")
	}
	lock := lockPath(&plan)
	defer os.Remove(lock)
	_ = os.WriteFile(filepath.Join(plan.Directory, "installer.pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
	// Do not destroy staging/backup files after a failure. They allow manual
	// recovery even if rollback itself encounters a filesystem failure.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for os.Getppid() == plan.ParentPID {
		select {
		case <-timer.C:
			return fmt.Errorf("GUI did not exit; old installation retained")
		case <-ticker.C:
		}
	}
	ack := filepath.Join(plan.Directory, "ready")
	err = replace(&plan, func() error { return launchAndWait(plan, ack) })
	if err != nil {
		old := exec.Command(plan.Executable, plan.Arguments...)
		old.Env = append(os.Environ(), "WATER_UPDATE_ERROR="+err.Error())
		if startErr := old.Start(); startErr != nil {
			return fmt.Errorf("update failed: %v; old GUI restart failed: %w", err, startErr)
		}
		_ = old.Process.Release()
		return err
	}
	return os.RemoveAll(plan.Directory)
}

func launchAndWait(plan Plan, ack string) error {
	cmd := exec.Command(plan.Executable, plan.Arguments...)
	cmd.Env = append(os.Environ(), "WATER_UPDATE_ACK="+ack)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return fmt.Errorf("new GUI exited before becoming ready: %v", err)
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
			return fmt.Errorf("new GUI did not become ready")
		case <-ticker.C:
			if _, err := os.Stat(ack); err == nil {
				return nil
			}
		}
	}
}

// AcknowledgeStartup is called off the render thread after its first live
// frame. Only the installer supplies this private, temporary handshake path.
func AcknowledgeStartup() {
	if path := os.Getenv("WATER_UPDATE_ACK"); path != "" {
		_ = os.Unsetenv("WATER_UPDATE_ACK")
		_ = os.WriteFile(path, []byte("ready\n"), 0600)
	}
}
