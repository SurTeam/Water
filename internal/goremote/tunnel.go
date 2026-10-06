package goremote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const (
	startTimeout  = 12 * time.Second
	retryInterval = 50 * time.Millisecond
)

type Tunnel struct {
	destination   string
	localSocket   string
	controlSocket string
	remoteSocket  string
	forwardSpec   string
}

type serverInfo struct {
	BuildVariant    string `json:"build_variant"`
	ServerVersion   string `json:"server_version"`
	ProtocolVersion uint32 `json:"protocol_version"`
	APISignature    string `json:"api_signature"`
}

func ValidateDestination(destination string) (string, error) {
	destination = strings.TrimSpace(destination)
	if destination == "" || strings.HasPrefix(destination, "-") || len(destination) > 255 {
		return "", errors.New("SSH destination must not be empty, start with '-', or exceed 255 bytes")
	}
	for _, r := range destination {
		if r < 0x20 || r == 0x7f || r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return "", errors.New("SSH destination must not contain whitespace or control characters")
		}
	}
	return destination, nil
}

func Connect(destination string) (*Tunnel, error) {
	destination, err := ValidateDestination(destination)
	if err != nil {
		return nil, err
	}
	remote := remoteControlSocket(destination)
	control := controlMasterSocket(destination)
	local := filepath.Join(
		sshSocketDirectory(),
		fmt.Sprintf("water-go-ssh-%d-%s.sock", os.Geteuid(), strings.ReplaceAll(uuid.New().String(), "-", "")),
	)
	_ = os.Remove(local)

	if err := ensureControlMaster(destination, control); err != nil {
		return nil, err
	}
	spec := local + ":" + remote
	if err := runSSH("-S", control, "-O", "forward", "-o", "ExitOnForwardFailure=yes", "-L", spec, destination); err != nil {
		return nil, err
	}

	t := &Tunnel{
		destination:   destination,
		localSocket:   local,
		controlSocket: control,
		remoteSocket:  remote,
		forwardSpec:   spec,
	}
	if info, err := t.Client().Inspect(time.Second); err == nil {
		if info.BuildVariant != gobuild.Variant {
			_ = t.Close()
			return nil, errors.New("remote server variant mismatch")
		}
		return t, nil // OpenSession negotiates capabilities, including diagnostic mode.
	}
	if os.Getenv("WATER_REMOTE_CONTROL_SOCKET") == "" {
		legacy, err := legacyRemoteSockets(destination, control)
		if err != nil {
			_ = t.Close()
			return nil, err
		}
		var found *Tunnel
		for _, candidate := range legacy {
			probe, err := forwardSocket(destination, control, candidate)
			if err != nil {
				if found != nil {
					_ = found.Close()
				}
				_ = t.Close()
				return nil, err
			}
			info, err := probe.Client().Inspect(time.Second)
			if err != nil {
				_ = probe.Close()
				if found != nil {
					_ = found.Close()
				}
				_ = t.Close()
				return nil, fmt.Errorf("legacy server at %s cannot be inspected; keep it running and migrate explicitly: %w", candidate, err)
			}
			if info.BuildVariant != gobuild.Variant {
				_ = probe.Close()
				continue
			}
			if found != nil {
				_ = found.Close()
				_ = probe.Close()
				_ = t.Close()
				return nil, errors.New("multiple legacy remote servers found; choose one with WATER_REMOTE_CONTROL_SOCKET")
			}
			found = probe
		}
		if found != nil {
			_ = t.Close()
			return found, nil
		}
	}

	if err := startRemoteServer(destination, control, remote); err != nil {
		_ = t.Close()
		return nil, err
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if info, err := t.Client().Inspect(time.Second); err == nil && info.BuildVariant == gobuild.Variant {
			return t, nil
		}
		time.Sleep(retryInterval)
	}
	_ = t.Close()
	return nil, fmt.Errorf("remote Water server at %s did not become ready within %s", destination, startTimeout)
}

func (t *Tunnel) LocalSocket() string  { return t.localSocket }
func (t *Tunnel) RemoteSocket() string { return t.remoteSocket }
func (t *Tunnel) Destination() string  { return t.destination }
func (t *Tunnel) Client() *goclient.Client {
	return goclient.New(t.localSocket)
}

func (t *Tunnel) compatible() bool {
	info, err := t.Client().Inspect(time.Second)
	if err != nil {
		return false
	}
	return goprotocol.Assess(goclient.Descriptor(gobuild.Variant), info.Descriptor).Compatible
}

func (t *Tunnel) Close() error {
	_ = runSSH("-S", t.controlSocket, "-O", "cancel", "-L", t.forwardSpec, t.destination)
	if err := os.Remove(t.localSocket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func controlMasterSocket(destination string) string {
	return filepath.Join(
		sshSocketDirectory(),
		fmt.Sprintf("water-go-ssh-%s-%d-%016x.ctl", gobuild.Variant, os.Geteuid(), stableID(destination+"|"+os.Getenv("WATER_SSH_CONFIG"))),
	)
}

func remoteControlSocket(destination string) string {
	if p := os.Getenv("WATER_REMOTE_CONTROL_SOCKET"); p != "" {
		return p
	}
	return filepath.Join(
		"/tmp",
		fmt.Sprintf("water-go-%s-%016x.sock", buildIdentityToken(), stableID(destination)),
	)
}

// OpenSSH adds a random suffix to ControlPath; Darwin permits only 104 bytes.
func sshSocketDirectory() string {
	if directory := os.TempDir(); len(directory) <= 24 {
		return directory
	}
	return "/tmp"
}

func buildIdentityToken() string {
	value := strings.ToLower(strings.TrimSpace(gobuild.Variant))
	if value == "" {
		value = "dev"
	}
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "dev"
	}
	return b.String()
}

func stableID(value string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(value))
	return h.Sum64()
}

func ensureControlMaster(destination, control string) error {
	if runSSH("-S", control, "-O", "check", destination) == nil {
		return nil
	}
	_ = os.Remove(control)
	return runSSH(
		"-M", "-N", "-f",
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=600",
		"-o", "BatchMode=yes",
		"-o", "Compression=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=2",
		"-o", "ConnectTimeout=10",
		"-S", control,
		destination,
	)
}

func startRemoteServer(destination, control, remoteSocket string) error {
	program := os.Getenv("WATER_REMOTE_SERVER_COMMAND")
	embedded := false

	if program == "" {
		target, err := detectRemoteTarget(destination, control)
		if err != nil {
			return err
		}
		payload, ok := embeddedServerPayload(target)
		if !ok {
			return fmt.Errorf(
				"no embedded Go server payload for %s/%s; run scripts/build-go-embedded-servers.sh before packaging",
				target.OS,
				target.Arch,
			)
		}
		program, err = deployEmbeddedServer(destination, control, target, payload)
		if err != nil {
			return err
		}
		embedded = true
	}

	if !embedded && strings.ContainsAny(program, " \t\r\n'\";$&|<>") {
		return errors.New("WATER_REMOTE_SERVER_COMMAND must be a simple remote executable path")
	}

	probe, err := boundedSSHOutput(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, program+" --server-info"))
	if err != nil {
		return fmt.Errorf("inspect remote server executable: %w", err)
	}
	var descriptor goprotocol.Descriptor
	if err := json.Unmarshal(probe, &descriptor); err != nil {
		return fmt.Errorf("inspect remote server descriptor: %w", err)
	}
	if descriptor.BuildVariant != gobuild.Variant || descriptor.ProtocolVersion != goprotocol.ProtocolVersion || descriptor.APISignature != goprotocol.APISignature || descriptor.ServerRevision != gobuild.ServerRevision {
		return errors.New("remote server payload does not match GUI variant, protocol or server revision")
	}
	remoteQuoted := shellQuote(remoteSocket)
	var commandText string
	if embedded {
		// program begins with $HOME and is composed only from fixed build
		// identity components, so leave it unquoted to permit HOME expansion.
		commandText = fmt.Sprintf(
			"%s --socket %s --empty-workspace >/tmp/water-go-%s-server.log 2>&1 </dev/null &",
			program,
			remoteQuoted,
			buildIdentityToken(),
		)
	} else {
		commandText = fmt.Sprintf(
			"command -v %s >/dev/null 2>&1 || exit 127; %s --socket %s --empty-workspace >/tmp/water-go-%s-server.log 2>&1 </dev/null &",
			program,
			program,
			remoteQuoted,
			buildIdentityToken(),
		)
	}
	return runSSH("-S", control, "-o", "BatchMode=yes", destination, commandText)
}

func runSSH(args ...string) error {
	cmd := command("ssh", args...)
	out, err := boundedSSHOutput(cmd)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return errors.New(msg)
	}
	return nil
}

func boundedSSHOutput(cmd *exec.Cmd) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	bounded := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	bounded.Stdin, bounded.Env, bounded.Dir = cmd.Stdin, cmd.Env, cmd.Dir
	return bounded.CombinedOutput()
}

func command(name string, args ...string) *exec.Cmd {
	if name == "ssh" {
		if custom := os.Getenv("WATER_SSH_PROGRAM"); custom != "" {
			name = custom
		}
		if config := os.Getenv("WATER_SSH_CONFIG"); config != "" {
			args = append([]string{"-F", config}, args...)
		}
	}
	return exec.Command(name, args...)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func DebugPaths(destination string) (string, string, error) {
	d, err := ValidateDestination(destination)
	if err != nil {
		return "", "", err
	}
	return controlMasterSocket(d), remoteControlSocket(d), nil
}

func UserIDString() string { return strconv.Itoa(os.Geteuid()) }
