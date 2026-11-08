package goserver

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/SurTeam/Water/internal/goplatform"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	dir := goplatform.RuntimeDirectory()
	if len(dir) > 24 && runtime.GOOS != "windows" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("water-go-pty-%s-%s.sock", gobuild.Variant, goplatform.UserToken()))
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
		socket = filepath.Join(filepath.Dir(socket), fmt.Sprintf("water-pty-%s-%s-%x.sock", gobuild.Variant, goplatform.UserToken(), identity[:8]))
		// Preserve upgrades from the legacy shared supervisor. Close only this
		// server's panes there; another old server may still own other panes.
		if client, err := gosupervisor.Dial(legacy); err == nil {
			_ = client.Close()
			socket = legacy
			s.sharedSupervisor = true
		}
	}
	current, legacy, ids, err := ensureSupervisor(socket)
	if err != nil {
		return err
	}
	s.legacySupervisorSocket = legacy
	if err := s.registry.AttachSupervisors(current, legacy, ids); err != nil {
		return err
	}
	s.supervisorSocket = current
	return nil
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

// ensureSupervisor returns the supervisor that must own new shells, plus the
// supervisor that still owns programs started before its binary was replaced.
// On macOS that replacement drops Full Disk Access: TCC checks the signature
// now at the process path, and a supervisor kept across the upgrade fails it.
func ensureSupervisor(socket string) (current, legacy string, legacyIDs []string, err error) {
	client, dialErr := gosupervisor.Dial(socket)
	if dialErr != nil {
		return socket, "", nil, startSupervisor(socket, "")
	}
	replaced := supervisorReplaced(client)
	terms, listErr := client.List()
	_ = client.Close()
	if !replaced {
		return socket, "", nil, nil
	}
	_ = terms
	_ = listErr
	if listErr == nil {
		for _, term := range terms {
			if !term.Exited {
				legacyIDs = append(legacyIDs, term.ID)
			}
		}
	}
	executable, err := supervisorExecutable()
	if err != nil {
		return "", "", nil, err
	}
	next := supervisorGenerationSocket(socket, executable)
	if next == socket {
		next += ".new"
	}
	if gen, genErr := gosupervisor.Dial(next); genErr == nil {
		stale := supervisorReplaced(gen)
		_ = gen.Close()
		if !stale {
			return next, socket, legacyIDs, nil
		}
	}
	return next, socket, legacyIDs, startSupervisor(next, executable)
}

// supervisorGenerationSocket names the supervisor for this exact binary, so the
// next in-place upgrade does not keep using the previous generation.
func supervisorGenerationSocket(base, executable string) string {
	info, err := os.Stat(executable)
	if err != nil {
		return base + ".new"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d", executable, info.ModTime().UnixNano(), info.Size())))
	ext := "." + hex.EncodeToString(sum[:4])
	next := base + ext
	if len(next) > 100 {
		keep := 100 - len(ext)
		if keep < 1 {
			keep = 1
		}
		next = base[:keep] + ext
	}
	return next
}

func startSupervisor(socket, executable string) error {
	if executable == "" {
		var err error
		executable, err = supervisorExecutable()
		if err != nil {
			return err
		}
	}
	command := exec.Command(executable, "--pty-supervisor", "--socket", socket)
	command.Env = os.Environ()
	command.Stdin = nil
	command.Stdout = nil
	command.Stderr = nil
	// A new session detaches the supervisor from this server's process group, so
	// stopping the server cannot signal the programs it is holding.
	goplatform.Detach(command)
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

// supervisorReplaced reports whether the running supervisor's binary changed
// after that process started. A supervisor that cannot identify itself is older
// than this check and is treated as replaced. The result is read once per
// connection; callers must not repeat it on the spawn path.
func supervisorReplaced(client *gosupervisor.Client) bool {
	executable, pid, err := client.Executable()
	if err != nil || executable == "" || pid <= 0 {
		return true
	}
	if current, currentErr := supervisorExecutable(); currentErr == nil && executable != current {
		// The running process reports the path it was started from. An in-place
		// upgrade replaces the file this server would start now.
		executable = current
	}
	replaced, err := goplatform.RunningBinaryReplaced(pid, executable)
	return err != nil || replaced
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

// SupervisorSocketForServer reports the socket that owns newly created shells.
func SupervisorSocketForServer(s *Server) string { return s.supervisorSocket }

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
		candidate := filepath.Join(filepath.Dir(self), goplatform.ExecutableName(name))
		info, statErr := os.Stat(candidate)
		if statErr == nil && !info.IsDir() && (runtime.GOOS == "windows" || info.Mode()&0o111 != 0) {
			return candidate, nil
		}
	}
	return self, nil
}
