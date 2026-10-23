package goserver

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/gosupervisor"
)

// supervisorSocketPath keeps one supervisor per user and build variant, so a
// restarted server finds the terminals its predecessor created.
func supervisorSocketPath() string {
	if explicit := os.Getenv("WATER_PTY_SUPERVISOR_SOCKET"); explicit != "" {
		return explicit
	}
	dir := os.TempDir()
	if len(dir) > 24 {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("water-go-pty-%s-%d.sock", gobuild.Variant, os.Geteuid()))
}

// EnableSupervisor makes this server own terminals through a PTY supervisor.
// When the supervisor is not running it is started detached, so it survives the
// server and keeps the programs inside panes alive across upgrades.
func (s *Server) EnableSupervisor() error {
	socket := supervisorSocketPath()
	if os.Getenv("WATER_PTY_SUPERVISOR_SOCKET") == "" {
		// Independent servers must never terminate each other's terminals.
		identity := sha256.Sum256([]byte(s.SocketPath))
		legacy := socket
		socket = filepath.Join(filepath.Dir(socket), fmt.Sprintf("water-pty-%s-%d-%x.sock", gobuild.Variant, os.Geteuid(), identity[:8]))
		// Preserve upgrades from the legacy shared supervisor. Close only this
		// server's panes there; another old server may still own other panes.
		if client, err := gosupervisor.Dial(legacy); err == nil {
			_ = client.Close()
			socket = legacy
			s.sharedSupervisor = true
		}
	}
	if err := ensureSupervisor(socket); err != nil {
		return err
	}
	return s.UseSupervisor(socket)
}

// EnableSupervisorFromEnv enables the supervisor unless WATER_PTY_SUPERVISOR is
// "off". Both the standalone server and the server a GUI embeds call this, so a
// local restart resumes panes the same way a remote upgrade does.
func (s *Server) EnableSupervisorFromEnv() error {
	if os.Getenv("WATER_PTY_SUPERVISOR") == "off" {
		return nil
	}
	return s.EnableSupervisor()
}

// UseSupervisor attaches to an already running supervisor socket. Tests start the
// supervisor in-process because the test binary is not water-server.
func (s *Server) UseSupervisor(socket string) error {
	if err := s.registry.AttachSupervisor(socket); err != nil {
		return err
	}
	s.supervisorSocket = socket
	return nil
}

// ensureSupervisor starts a detached supervisor when the socket is not served.
func ensureSupervisor(socket string) error {
	if client, err := gosupervisor.Dial(socket); err == nil {
		_ = client.Close()
		return nil
	}
	executable, err := supervisorExecutable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "--pty-supervisor", "--socket", socket)
	command.Env = os.Environ()
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	// A new session detaches the supervisor from this server's process group, so
	// stopping the server cannot signal the programs it is holding.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client, err := gosupervisor.Dial(socket); err == nil {
			_ = client.Close()
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return errors.New("PTY supervisor did not become ready")
}

// detachSupervisorOnClose releases terminals without terminating their programs.
func (s *Server) detachSupervisor() {
	if s.supervisorSocket == "" {
		return
	}
	s.registry.DetachSupervisor()
}

// SupervisorSocketForTest reports the socket a supervised server uses.
func SupervisorSocketForTest() string { return supervisorSocketPath() }

// supervisorExecutable finds a binary that serves the supervisor role. A GUI
// embeds the server in its own process, so its own executable may not accept
// --pty-supervisor; the standalone server next to it does.
func supervisorExecutable() (string, error) {
	if explicit := os.Getenv("WATER_SUPERVISOR_EXECUTABLE"); explicit != "" {
		return explicit, nil
	}
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	names := []string{"water-pty", "water-server"}
	if gobuild.Variant == "dev" {
		names = []string{"water-pty-dev", "water-srv-dev", "water-server"}
	}
	for _, name := range names {
		candidate := filepath.Join(filepath.Dir(self), name)
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return self, nil
}
