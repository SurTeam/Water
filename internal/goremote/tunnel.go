package goremote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	warnings      string
	diagnostics   *sshDiagnostics
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

func Connect(destination string) (*Tunnel, error) { return connect(destination, nil) }

func ConnectInteractive(destination string, prompt Prompt) (*Tunnel, error) {
	return connect(destination, prompt)
}

func connect(destination string, prompt Prompt) (result *Tunnel, resultErr error) {
	diagnostics := &sshDiagnostics{}
	defer func() {
		if result != nil {
			result.warnings = diagnostics.String()
			result.diagnostics = nil
		}
		if resultErr != nil && diagnostics.String() != "" {
			resultErr = fmt.Errorf("%w\n\nSSH diagnostics:\n%s", resultErr, diagnostics.String())
		}
	}()
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

	_, err = ensureControlMasterInteractive(destination, control, prompt, diagnostics)
	if err != nil {
		return nil, err
	}
	spec := local + ":" + remote
	if err := diagnostics.run("-S", control, "-O", "forward", "-o", "ExitOnForwardFailure=yes", "-L", spec, destination); err != nil {
		return nil, err
	}

	t := &Tunnel{
		destination:   destination,
		localSocket:   local,
		controlSocket: control,
		remoteSocket:  remote,
		forwardSpec:   spec,
		diagnostics:   diagnostics,
	}
	if info, err := t.Client().Inspect(time.Second); err == nil {
		if info.BuildVariant != gobuild.Variant {
			_ = t.Close()
			return nil, errors.New("remote server variant mismatch")
		}
		return t, nil // OpenSession negotiates capabilities, including diagnostic mode.
	}
	if os.Getenv("WATER_REMOTE_CONTROL_SOCKET") == "" {
		legacy, err := legacyRemoteSockets(destination, control, diagnostics)
		if err != nil {
			_ = t.Close()
			return nil, err
		}
		var found *Tunnel
		for _, candidate := range legacy {
			probe, err := forwardSocket(destination, control, candidate, diagnostics)
			if probe != nil {
				probe.diagnostics = diagnostics
			}
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

	if err := startRemoteServer(destination, control, remote, diagnostics); err != nil {
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

func (t *Tunnel) Warnings() string { return t.warnings }

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
	if t.diagnostics != nil {
		_ = t.diagnostics.run("-S", t.controlSocket, "-O", "cancel", "-L", t.forwardSpec, t.destination)
	} else {
		_ = runSSH("-S", t.controlSocket, "-O", "cancel", "-L", t.forwardSpec, t.destination)
	}
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
	_, err := ensureControlMasterInteractive(destination, control, nil)
	return err
}

func ensureControlMasterInteractive(destination, control string, prompt Prompt, collectors ...*sshDiagnostics) (string, error) {
	d := diagnosticCollector(collectors)
	if d.run("-S", control, "-O", "check", destination) == nil {
		return "", nil
	}
	_ = os.Remove(control)
	args := []string{"-M", "-N", "-f", "-o", "ControlMaster=yes", "-o", "ControlPersist=600", "-o", "BatchMode=yes", "-o", "Compression=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2", "-o", "ConnectTimeout=10", "-S", control, destination}
	out, err := d.output(command("ssh", args...), startTimeout)
	if err == nil {
		return d.String(), nil
	}
	first := sshFailure(out, err).Error()
	// Strip the local process status prefix before matching OpenSSH diagnostics.
	authText := first
	if _, detail, ok := strings.Cut(authText, ": "); ok && strings.HasPrefix(authText, "exit status ") {
		authText = detail
	}
	// Only authentication rejection warrants asking for credentials. Configuration,
	// host-key and network failures retain their complete original diagnostics.
	var exit *exec.ExitError
	if prompt == nil || !errors.As(err, &exit) || exit.ExitCode() != 255 || !authenticationRejected(authText) {
		return "", sshFailure(out, err)
	}
	env, cleanup, setupErr := newAskpass(prompt)
	if setupErr != nil {
		return "", setupErr
	}
	defer cleanup()
	for i := range args {
		if args[i] == "BatchMode=yes" {
			args[i] = "BatchMode=no"
		}
	}
	args = append([]string{"-o", "PreferredAuthentications=keyboard-interactive,password", "-o", "NumberOfPasswordPrompts=3"}, args...)
	cmd := command("ssh", args...)
	cmd.Env = env
	out, err = d.output(cmd, 3*time.Minute)
	if err != nil {
		return "", fmt.Errorf("%s\n\nPassword authentication: %w", first, sshFailure(out, err))
	}
	return d.String(), nil
}

func sshFailure(out []byte, err error) error {
	if msg := strings.TrimSpace(string(out)); msg != "" {
		return fmt.Errorf("%s: %w", msg, err)
	}
	return err
}

func interactiveSSHOutput(cmd *exec.Cmd) ([]byte, error) { return sshOutput(cmd, 3*time.Minute) }

func startRemoteServer(destination, control, remoteSocket string, collectors ...*sshDiagnostics) error {
	d := diagnosticCollector(collectors)
	program := os.Getenv("WATER_REMOTE_SERVER_COMMAND")
	embedded := false

	if program == "" {
		target, err := detectRemoteTarget(destination, control, d)
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
		program, err = deployEmbeddedServer(destination, control, target, payload, d)
		if err != nil {
			return err
		}
		embedded = true
	}

	if !embedded && strings.ContainsAny(program, " \t\r\n'\";$&|<>") {
		return errors.New("WATER_REMOTE_SERVER_COMMAND must be a simple remote executable path")
	}

	probe, err := d.output(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, program+" --server-info"), startTimeout)
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
	return d.run("-S", control, "-o", "BatchMode=yes", destination, commandText)
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
	return sshOutput(cmd, startTimeout)
}

func sshOutput(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
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

// sshDiagnostics belongs to one connection attempt; stdout remains parseable.
type sshDiagnostics struct{ warnings []string }

func diagnosticCollector(values []*sshDiagnostics) *sshDiagnostics {
	if len(values) > 0 && values[0] != nil {
		return values[0]
	}
	return &sshDiagnostics{}
}
func (d *sshDiagnostics) String() string { return strings.Join(d.warnings, "\n") }
func (d *sshDiagnostics) output(cmd *exec.Cmd, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	bounded := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	bounded.Stdin, bounded.Env, bounded.Dir = cmd.Stdin, cmd.Env, cmd.Dir
	var stdout, stderr bytes.Buffer
	bounded.Stdout = &stdout
	bounded.Stderr = &stderr
	err := bounded.Run()
	detail := strings.TrimSpace(stderr.String())
	if err != nil {
		if detail != "" {
			err = fmt.Errorf("%w: %s", err, detail)
		}
		return stdout.Bytes(), err
	}
	if detail != "" {
		d.warnings = append(d.warnings, detail)
	}
	return stdout.Bytes(), nil
}
func (d *sshDiagnostics) run(args ...string) error {
	out, err := d.output(command("ssh", args...), startTimeout)
	if err != nil {
		return sshFailure(out, err)
	}
	return nil
}

var authRejection = regexp.MustCompile(`(?m)^(?:[^\s:]+: )?Permission denied \((?:publickey|password|keyboard-interactive|gssapi-with-mic|gssapi-keyex|hostbased|none)(?:,(?:publickey|password|keyboard-interactive|gssapi-with-mic|gssapi-keyex|hostbased|none))*\)\.\r?$`)

func authenticationRejected(message string) bool { return authRejection.MatchString(message) }
