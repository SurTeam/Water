package goupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goplatform"
)

const InstallerArgument = "--internal-install-update"

func lockPath(plan *Plan) string {
	return filepath.Join(filepath.Dir(plan.Directory), "."+filepath.Base(plan.Executable)+"-update-lock")
}

// StartInstaller transfers ownership of the staging directory to a separate
// process. Installation starts only after the GUI has exited normally.
func StartInstaller(plan *Plan) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("in-app update installation is unavailable on Windows")
	}
	guard, err := acquireGuard(lockPath(plan))
	if err != nil {
		return fmt.Errorf("another update is installing: %w", err)
	}
	defer guard.Close()
	if err = recoverVisible(plan); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = os.Remove(lockPath(plan))
		}
	}()
	owner, _ := json.Marshal(struct{ PID int }{os.Getpid()})
	if err = os.WriteFile(lockPath(plan), owner, 0600); err != nil {
		return err
	}
	helper := filepath.Join(plan.Directory, filepath.Base(plan.Helper))
	if err = copyExecutable(plan.Helper, helper); err != nil {
		return err
	}
	defer func() {
		if !started {
			_ = os.Remove(helper)
		}
	}()
	data, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	path := filepath.Join(plan.Directory, "plan.json")
	if err = os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(plan.Directory, "install.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	readyR, readyW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer readyR.Close()
	defer readyW.Close()
	commitR, commitW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer commitR.Close()
	defer commitW.Close()
	cmd := exec.Command(helper, InstallerArgument, path)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "WATER_UPDATE_ACK=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.ExtraFiles = []*os.File{guard, readyW, commitR}
	cmd.Stdout = log
	cmd.Stderr = log
	goplatform.Detach(cmd)
	if err = cmd.Start(); err != nil {
		return err
	}
	readyW.Close()
	commitR.Close()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	failed := true
	defer func() {
		if failed {
			_ = cmd.Process.Kill()
			<-done
		}
	}()
	_ = readyR.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ack [1]byte
	if _, err = io.ReadFull(readyR, ack[:]); err != nil || ack[0] != 'R' {
		return fmt.Errorf("installer did not acknowledge takeover: %v", err)
	}
	if _, err = commitW.Write([]byte("C")); err != nil {
		return err
	}
	if _, err = io.ReadFull(readyR, ack[:]); err != nil || ack[0] != 'T' {
		return fmt.Errorf("installer did not commit takeover: %v", err)
	}
	failed = false
	started = true
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
	copied := false
	defer func() {
		if !copied {
			_ = os.Remove(destination)
		}
	}()
	_, err = io.Copy(dst, src)
	closeErr := dst.Close()
	if err != nil {
		return err
	}
	if closeErr == nil {
		copied = true
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
				return fmt.Errorf("%w: replacement failed: %v; rollback failed: %v", errRollbackFailed, err, recovery)
			}
			return err
		}
		moved = append(moved, movedEntry{entry: e, backup: backup})
		if err := os.Rename(e.Source, e.Target); err != nil {
			if recovery := rollback(); recovery != nil {
				return fmt.Errorf("%w: replacement failed: %v; rollback failed: %v", errRollbackFailed, err, recovery)
			}
			return err
		}
		moved[len(moved)-1].installed = true
	}
	if err := launch(); err != nil {
		if errors.Is(err, errReplacementAlive) {
			return err
		}
		if recovery := rollback(); recovery != nil {
			return fmt.Errorf("%w: startup failed: %v; rollback failed: %v", errRollbackFailed, err, recovery)
		}
		return err
	}
	return nil
}

func RunInstaller(path string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("in-app update installation is unavailable on Windows")
	}
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
	guard, err := inheritedGuard()
	if err != nil {
		return err
	}
	defer guard.Close()
	ready := os.NewFile(4, "takeover ready")
	commit := os.NewFile(5, "takeover commit")
	if ready == nil || commit == nil {
		return fmt.Errorf("missing takeover pipes")
	}
	defer ready.Close()
	defer commit.Close()
	logger := log.New(os.Stderr, "update: ", log.LstdFlags)
	keeper, err := openInstallerKeeper(plan.Arguments)
	if err != nil {
		return fmt.Errorf("preserve server before takeover: %w", err)
	}
	if keeper != nil {
		defer keeper.Close()
	}
	logger.Print("helper awaiting transfer")
	if _, err = ready.Write([]byte("R")); err != nil {
		return err
	}
	_ = commit.SetReadDeadline(time.Now().Add(5 * time.Second))
	var token [1]byte
	if _, err = io.ReadFull(commit, token[:]); err != nil || token[0] != 'C' {
		return fmt.Errorf("transfer cancelled: %v", err)
	}
	owner, _ := json.Marshal(struct{ PID int }{os.Getpid()})
	if err = os.WriteFile(lockPath(&plan), owner, 0600); err != nil {
		return err
	}
	defer os.Remove(lockPath(&plan))
	_ = os.WriteFile(filepath.Join(plan.Directory, "installer.pid"), []byte(fmt.Sprint(os.Getpid())), 0600)
	if _, err = ready.Write([]byte("T")); err != nil {
		return err
	}
	ready.Close()
	commit.Close()
	logger.Print("ownership transferred; waiting for GUI exit")
	deadline := time.Now().Add(30 * time.Second)
	for processAlive(plan.ParentPID) {
		if time.Now().After(deadline) {
			return fmt.Errorf("GUI did not exit; old installation retained")
		}
		time.Sleep(50 * time.Millisecond)
	}
	logger.Print("parent exited; replacing installation")
	ack := filepath.Join(plan.Directory, "ready")
	err = replace(&plan, func() error { return launchAndWait(plan, ack) })
	if err != nil {
		if !canRestartRestored(err) {
			return err
		}
		logger.Printf("replacement failed; launching restored GUI: %v", err)
		if startErr := launchAndWaitWithError(plan, filepath.Join(plan.Directory, "rollback-ready"), err.Error()); startErr != nil {
			return fmt.Errorf("update failed: %v; old GUI restart failed: %w", err, startErr)
		}
		return err
	}
	return os.RemoveAll(plan.Directory)
}

var errRollbackFailed = errors.New("rollback incomplete; automatic restart disabled")

func canRestartRestored(err error) bool {
	return !errors.Is(err, errReplacementAlive) && !errors.Is(err, errRollbackFailed)
}

// OpenSession only dials the explicitly selected server; it never starts one.
func openInstallerKeeper(args []string) (*goclient.Session, error) {
	socket := ""
	for i := 0; i < len(args); i++ {
		for _, flag := range []string{"--control-socket", "--socket"} {
			if args[i] == flag {
				if i+1 >= len(args) || args[i+1] == "" {
					return nil, fmt.Errorf("missing %s value", flag)
				}
				socket = args[i+1]
				i++
				break
			}
			if strings.HasPrefix(args[i], flag+"=") {
				socket = strings.TrimPrefix(args[i], flag+"=")
				if socket == "" {
					return nil, fmt.Errorf("missing %s value", flag)
				}
			}
		}
	}
	if socket == "" {
		return nil, nil
	}
	return goclient.New(socket).OpenSession()
}

var errReplacementAlive = errors.New("replacement could not be stopped; backups retained")

func launchAndWait(plan Plan, ack string) error { return launchAndWaitWithError(plan, ack, "") }
func launchAndWaitWithError(plan Plan, ack, message string) error {
	return launchAndWaitOptions(plan, ack, message, message != "", 20*time.Second)
}
func launchAndWaitOptions(plan Plan, ack, message string, legacy bool, timeout time.Duration) error {
	_ = os.Remove(ack)
	_ = os.Remove(ack + ".pid")
	env := append(os.Environ(), "WATER_UPDATE_ACK="+ack, PreserveServerEnv+"=1", "WATER_UPDATE_ERROR="+message)
	cmd := exec.Command(plan.Executable, plan.Arguments...)
	if runtime.GOOS == "darwin" {
		if i := strings.Index(plan.Executable, ".app/Contents/MacOS/"); i >= 0 {
			args := []string{"-n", "-W", "--env", "WATER_UPDATE_ACK=" + ack, "--env", PreserveServerEnv + "=1", "--env", "WATER_UPDATE_ERROR=" + message, plan.Executable[:i+4], "--args"}
			cmd = exec.Command("/usr/bin/open", append(args, plan.Arguments...)...)
		}
	}
	cmd.Env = env
	log.Printf("update: launching %s; waiting for first frame", plan.Executable)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	stop := func() error {
		// The private startup marker identifies only the GUI started by this attempt.
		pid := cmd.Process.Pid
		if cmd.Path == "/usr/bin/open" {
			b, err := os.ReadFile(ack + ".pid")
			if err != nil {
				return errReplacementAlive
			}
			pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
			if err != nil || pid <= 1 {
				return errReplacementAlive
			}
		}
		p, err := os.FindProcess(pid)
		if err != nil {
			return errReplacementAlive
		}
		_ = p.Kill()
		limit := time.Now().Add(3 * time.Second)
		for processAlive(pid) && time.Now().Before(limit) {
			time.Sleep(25 * time.Millisecond)
		}
		if processAlive(pid) {
			return errReplacementAlive
		}
		return nil
	}
	for {
		// Check ACK before exit: a short-lived fixture may acknowledge and exit together.
		if b, err := os.ReadFile(ack); err == nil {
			if legacy && strings.TrimSpace(string(b)) == "ready" {
				log.Print("update: restored legacy GUI acknowledged first frame")
				return nil
			}
			pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
			owned := pid == cmd.Process.Pid
			if cmd.Path == "/usr/bin/open" {
				marker, readErr := os.ReadFile(ack + ".pid")
				owned = readErr == nil && strings.TrimSpace(string(marker)) == strconv.Itoa(pid)
			}
			if e == nil && pid > 1 && owned {
				log.Printf("update: first frame acknowledged by PID %d", pid)
				return nil
			}
		}
		select {
		case err := <-done:
			if cmd.Path == "/usr/bin/open" && err != nil {
				if stopErr := stop(); stopErr != nil {
					return stopErr
				}
			}
			return fmt.Errorf("GUI exited before first frame: %v", err)
		case <-timer.C:
			if err := stop(); err != nil {
				return err
			}
			return fmt.Errorf("GUI did not acknowledge first frame")
		case <-tick.C:
		}
	}
}

// The startup capability belongs only to this process. Capture it before any
// GUI child can inherit the environment, and serialize first-frame callbacks.
var startupACK struct {
	sync.Mutex
	path         string
	acknowledged bool
}

func init() {
	startupACK.path = os.Getenv("WATER_UPDATE_ACK")
	_ = os.Unsetenv("WATER_UPDATE_ACK")
	if startupACK.path != "" {
		_ = os.WriteFile(startupACK.path+".pid", []byte(strconv.Itoa(os.Getpid())), 0600)
	}
}

// AcknowledgeStartup completes the first-frame handshake before releasing server protection.
// Failed writes remain retryable; successful concurrent calls acknowledge only once.
func AcknowledgeStartup() {
	startupACK.Lock()
	defer startupACK.Unlock()
	if startupACK.acknowledged {
		return
	}
	if startupACK.path != "" {
		if err := os.WriteFile(startupACK.path, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			return
		}
	}
	startupACK.path = ""
	startupACK.acknowledged = true
	StartupAcknowledged()
}
