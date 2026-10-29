package goremote

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/SurTeam/Water/internal/gobridge"
	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
)

// A portable tunnel owns its private listener and each SSH child. Remote server
// lifetime is independent: closing a view never terminates the remote server.
type portableTunnel struct {
	connections                  map[net.Conn]struct{}
	releaseNative                func()
	native                       *nativeSSH
	lastDiagnostic               *boundedDiagnostic
	destination, program, remote string
	target                       RemoteTarget
	env                          []string
	diagnostics                  *sshDiagnostics
	listener                     net.Listener
	directory                    string
	mu                           sync.Mutex
	children                     map[*exec.Cmd]net.Conn
	closed                       bool
	cleanupAuth                  func()
}

func powerShell(script string) string {
	script = "$ProgressPreference='SilentlyContinue'; " + script
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, value := range units {
		binary.LittleEndian.PutUint16(data[i*2:], value)
	}
	return "powershell.exe -NoLogo -NoProfile -NonInteractive -EncodedCommand " + base64.StdEncoding.EncodeToString(data)
}
func psQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
func portableRequested(remote string) bool {
	return runtime.GOOS == "windows" || (os.Getenv("WATER_SSH_TRANSPORT") == "stdio" || os.Getenv("WATER_SSH_TRANSPORT") == "go") || strings.Contains(remote, `:\`)
}
func portableSSH(destination, script string, env []string, noInput ...bool) *exec.Cmd {
	batch := "yes"
	if env != nil {
		batch = "no"
	}
	args := []string{"-T", "-o", "ControlMaster=no", "-o", "BatchMode=" + batch, "-o", "ConnectTimeout=10", destination, script}
	if len(noInput) > 0 && noInput[0] {
		args = append([]string{"-n"}, args...)
	}
	if runtime.GOOS != "windows" {
		args = append([]string{"-S", controlMasterSocket(destination)}, args...)
	}
	cmd := command("ssh", args...)
	cmd.Env = env
	return cmd
}
func (p *portableTunnel) output(script string, input io.Reader) ([]byte, error) {
	if p.native != nil {
		timeout := 30 * time.Second
		if input != nil {
			timeout = 2 * time.Minute
		}
		out, detail, err := p.native.output(script, input, timeout)
		message := strings.TrimSpace(string(detail))
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, message)
		}
		if message != "" {
			p.diagnostics.warnings = append(p.diagnostics.warnings, message)
		}
		return out, nil
	}
	cmd := portableSSH(p.destination, script, p.env, input == nil)
	cmd.Stdin = input
	timeout := 30 * time.Second
	if input != nil {
		timeout = 2 * time.Minute
	}
	return p.diagnostics.output(cmd, timeout)
}
func parseTarget(output []byte) (RemoteTarget, error) {
	// Login profiles may print banners before the command. Only a tagged
	// platform record can bypass the strict legacy two-field response.
	const marker = "WATER_REMOTE_TARGET "
	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			output = []byte(strings.TrimPrefix(strings.TrimSpace(line), marker))
			break
		}
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 {
		return RemoteTarget{}, fmt.Errorf("invalid remote platform response %q", output)
	}
	target := RemoteTarget{OS: strings.ToLower(fields[0]), Arch: strings.ToLower(fields[1])}
	if target.OS != "windows" && target.OS != "linux" && target.OS != "darwin" {
		return RemoteTarget{}, fmt.Errorf("unsupported remote OS %q", target.OS)
	}
	switch target.Arch {
	case "x86_64", "amd64":
		target.Arch = "amd64"
	case "aarch64", "arm64":
		target.Arch = "arm64"
	default:
		return RemoteTarget{}, fmt.Errorf("unsupported remote architecture %q", target.Arch)
	}
	return target, nil
}
func (p *portableTunnel) detect() error {
	out, err := p.output("uname -s; uname -m", nil)
	if err == nil {
		p.target, err = parseTarget(out)
		return err
	}
	if authenticationRejected(err.Error()) {
		return err
	}
	out, err = p.output(powerShell("[Console]::WriteLine('WATER_REMOTE_TARGET windows '+$env:PROCESSOR_ARCHITECTURE)"), nil)
	if err != nil {
		return fmt.Errorf("detect remote platform: %w", err)
	}
	p.target, err = parseTarget(out)
	return err
}

// Unix clients probe only when deciding whether a Windows destination needs the
// portable transport. Failed/auth-required probes fall through to existing SSH.
func remoteWindows(destination string) bool {
	p := &portableTunnel{destination: destination, diagnostics: &sshDiagnostics{}}
	return p.detect() == nil && p.target.OS == "windows"
}
func newPortable(destination string, prompt Prompt) (*portableTunnel, error) {
	destination, err := ValidateDestination(destination)
	if err != nil {
		return nil, err
	}
	p := &portableTunnel{destination: destination, diagnostics: &sshDiagnostics{}, children: make(map[*exec.Cmd]net.Conn)}
	if runtime.GOOS == "windows" || os.Getenv("WATER_SSH_TRANSPORT") == "go" {
		p.native, p.releaseNative, err = acquireNativeSSH(destination, prompt)
		if err != nil {
			return nil, err
		}
	}
	err = p.detect()
	if err != nil && p.native == nil && prompt != nil && authenticationRejected(err.Error()) {
		// Independent SSH children cannot use ControlMaster on Windows. Cache replies
		// only for this tunnel, behind the same private askpass socket as Unix SSH.
		var mu sync.Mutex
		replies := map[string]string{}
		p.env, p.cleanupAuth, err = newAskpass(func(text string) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			if value, ok := replies[text]; ok {
				return value, nil
			}
			value, err := prompt(text)
			if err == nil {
				replies[text] = value
			}
			return value, err
		})
		if err == nil {
			err = p.detect()
		}
	}
	if err != nil {
		p.close()
		return nil, err
	}
	if err = p.provision(); err != nil {
		p.close()
		return nil, err
	}
	return p, nil
}
func (p *portableTunnel) invoke(arguments ...string) string {
	quote := shellQuote
	if p.target.OS == "windows" {
		quote = psQuote
	}
	values := []string{quote(p.program)}
	for _, arg := range arguments {
		values = append(values, quote(arg))
	}
	script := strings.Join(values, " ")
	if p.target.OS == "windows" {
		return powerShell("& " + script + "; exit $LASTEXITCODE")
	}
	return "exec " + script
}
func (p *portableTunnel) provision() error {
	if explicit := strings.TrimSpace(os.Getenv("WATER_REMOTE_SERVER_COMMAND")); explicit != "" {
		p.program = explicit
		return nil
	}
	payload, ok := embeddedServerPayload(p.target)
	if !ok {
		name := "water-server"
		if gobuild.Variant == "dev" {
			name = "water-srv-dev"
		}
		script := "command -v " + shellQuote(name)
		if p.target.OS == "windows" {
			script = powerShell("$ErrorActionPreference='Stop'; (Get-Command " + psQuote(name+".exe") + ").Source")
		}
		out, err := p.output(script, nil)
		if err != nil {
			return fmt.Errorf("no bundled %s/%s server and no matching remote server on PATH: %w", p.target.OS, p.target.Arch, err)
		}
		p.program = strings.TrimSpace(string(out))
		return nil
	}
	sum := sha256.Sum256(payload)
	token := fmt.Sprintf("%s-%x", buildIdentityToken(), sum[:12])
	if p.target.OS == "windows" {
		script := "$ErrorActionPreference='Stop'; $dir=Join-Path $env:LOCALAPPDATA 'Water\\ssh-payloads'; [IO.Directory]::CreateDirectory($dir) | Out-Null; $bin=Join-Path $dir " + psQuote(token+".exe") + "; [Console]::WriteLine($bin); if (Test-Path -LiteralPath $bin) { $hash=[BitConverter]::ToString([Security.Cryptography.SHA256]::Create().ComputeHash([IO.File]::ReadAllBytes($bin))).Replace('-',''); if ($hash -eq " + psQuote(fmt.Sprintf("%x", sum)) + ") { [Console]::WriteLine('verified') } }"
		out, err := p.output(powerShell(script), nil)
		if err != nil {
			return fmt.Errorf("prepare Windows server cache: %w", err)
		}
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		p.program = strings.TrimSpace(lines[0])
		if len(lines) > 1 && strings.TrimSpace(lines[1]) == "verified" {
			return nil
		}
		temporary := p.program + fmt.Sprintf(".%x.tmp", time.Now().UnixNano())
		if err = p.upload(payload, temporary); err != nil {
			return err
		}
		script = "$ErrorActionPreference='Stop'; $tmp=" + psQuote(temporary) + "; $bin=" + psQuote(p.program) + "; try { if ((Get-FileHash -LiteralPath $tmp -Algorithm SHA256).Hash -ne " + psQuote(fmt.Sprintf("%x", sum)) + ") { throw 'uploaded server digest mismatch' }; if (!(Test-Path -LiteralPath $bin)) { try { [IO.File]::Move($tmp,$bin) } catch { if (!(Test-Path -LiteralPath $bin)) { throw } } }; if ((Get-FileHash -LiteralPath $bin -Algorithm SHA256).Hash -ne " + psQuote(fmt.Sprintf("%x", sum)) + ") { throw 'cached server digest mismatch' } } finally { if (Test-Path -LiteralPath $tmp) { Remove-Item -LiteralPath $tmp -ErrorAction Stop } }; exit 0"
		if _, err = p.output(powerShell(script), nil); err != nil {
			return fmt.Errorf("install Windows server payload: %w", err)
		}

	} else {
		// Cache names contain only trusted build identity/hash characters.
		script := "set -eu; dir=\"$HOME/.cache/water-go/ssh-payloads\"; mkdir -p \"$dir\"; bin=\"$dir/" + token + "\"; tmp=\"$bin.tmp.$$\"; cat > \"$tmp\"; chmod 700 \"$tmp\"; if test -f \"$bin\"; then rm -f \"$tmp\"; else mv \"$tmp\" \"$bin\"; fi; printf '%s\\n' \"$bin\""
		out, err := p.output(script, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		p.program = strings.TrimSpace(string(out))
	}
	return nil
}
func (p *portableTunnel) endpoint(destination string) (string, error) {
	if explicit := os.Getenv("WATER_REMOTE_CONTROL_SOCKET"); explicit != "" {
		return explicit, nil
	}
	if p.target.OS != "windows" {
		return remoteControlSocket(destination), nil
	}
	name := fmt.Sprintf("ssh-%s-%016x.sock", buildIdentityToken(), stableID(destination))
	out, err := p.output(powerShell("[Console]::WriteLine((Join-Path $env:LOCALAPPDATA "+psQuote("Water\\run\\"+name)+"))"), nil)
	return strings.TrimSpace(string(out)), err
}
func (p *portableTunnel) occupied() (bool, error) {
	out, err := p.output(p.invoke("--endpoint-status", "--socket", p.remote), nil)
	if err != nil {
		return false, fmt.Errorf("inspect remote endpoint ownership: %w", err)
	}
	switch strings.TrimSpace(string(out)) {
	case "occupied":
		return true, nil
	case "free":
		return false, nil
	}
	return false, fmt.Errorf("invalid remote endpoint status %q", out)
}
func (p *portableTunnel) start() error {
	info, err := p.output(p.invoke("--server-info"), nil)
	if err != nil {
		return fmt.Errorf("inspect candidate server: %w", err)
	}
	var descriptor goprotocol.Descriptor
	if json.Unmarshal(info, &descriptor) != nil || !goprotocol.Assess(goclient.Descriptor(gobuild.Variant), descriptor).Compatible {
		return errors.New("candidate remote server has an incompatible variant or protocol")
	}

	out, err := p.output(p.invoke("--daemonize", "--empty-workspace", "--socket", p.remote), nil)
	if err != nil {
		return fmt.Errorf("start remote Water server: %w: %s", err, out)
	}
	return nil
}
func (p *portableTunnel) listen() (string, error) {
	if err := os.MkdirAll(sshSocketDirectory(), 0700); err != nil {
		return "", err
	}
	directory, err := os.MkdirTemp(sshSocketDirectory(), "wssh-")
	if err != nil {
		return "", err
	}
	local := filepath.Join(directory, "s")
	listener, err := net.Listen("unix", local)
	if err != nil {
		os.RemoveAll(directory)
		return "", err
	}
	p.directory, p.listener = directory, listener
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go p.serve(conn)
		}
	}()
	return local, nil
}
func (p *portableTunnel) serve(conn net.Conn) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		conn.Close()
		return
	}
	remote := p.remote
	if p.connections == nil {
		p.connections = make(map[net.Conn]struct{})
	}
	p.connections[conn] = struct{}{}
	p.mu.Unlock()
	defer func() { conn.Close(); p.mu.Lock(); delete(p.connections, conn); p.mu.Unlock() }()
	if p.native != nil {
		p.serveNative(conn, remote)
		return
	}
	cmd := portableSSH(p.destination, p.invoke("--stdio-bridge", "--socket", remote), p.env)
	input, err := cmd.StdoutPipe()
	if err != nil {
		conn.Close()
		return
	}
	output, err := cmd.StdinPipe()
	if err != nil {
		input.Close()
		conn.Close()
		return
	}
	diagnostic := &boundedDiagnostic{}
	cmd.Stderr = diagnostic
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		input.Close()
		output.Close()
		conn.Close()
		return
	}
	if err = cmd.Start(); err != nil {
		p.mu.Unlock()
		input.Close()
		output.Close()
		conn.Close()
		return
	}
	p.children[cmd] = conn
	p.lastDiagnostic = diagnostic
	p.mu.Unlock()
	_ = gobridge.Relay(conn, input, output)
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	p.mu.Lock()
	delete(p.children, cmd)
	p.mu.Unlock()
}

type boundedDiagnostic struct {
	mu   sync.Mutex
	text []byte
}

func (b *boundedDiagnostic) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	b.text = append(b.text, data...)
	if len(b.text) > 32768 {
		b.text = append([]byte(nil), b.text[len(b.text)-32768:]...)
	}
	return n, nil
}
func (p *portableTunnel) close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	if p.listener != nil {
		p.listener.Close()
	}
	for connection := range p.connections {
		connection.Close()
	}
	for child, conn := range p.children {
		conn.Close()
		_ = child.Process.Kill()
	}
	p.mu.Unlock()
	if p.releaseNative != nil {
		p.releaseNative()
	}
	if p.directory != "" {
		os.RemoveAll(p.directory)
	}
	if p.cleanupAuth != nil {
		p.cleanupAuth()
	}
	return nil
}
func connectPortable(destination string, prompt Prompt, remote string, existing bool) (*Tunnel, error) {
	p, err := newPortable(destination, prompt)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			p.close()
		}
	}()
	if remote == "" {
		remote, err = p.endpoint(destination)
		if err != nil {
			return nil, err
		}
	}
	p.remote = remote
	local, err := p.listen()
	if err != nil {
		return nil, err
	}
	t := &Tunnel{destination: destination, localSocket: local, remoteSocket: remote, portable: p}
	if existing {
		ok = true
		return t, nil
	}
	inspect := func() (*goprotocol.ServerInfo, error) {
		info, err := t.Client().Inspect(8 * time.Second)
		return &info, err
	}
	info, probeErr := inspect()
	if probeErr == nil && goprotocol.Assess(goclient.Descriptor(gobuild.Variant), info.Descriptor).Compatible {
		t.warnings = p.diagnostics.String()
		ok = true
		return t, nil
	}
	occupied, err := p.occupied()
	if err != nil {
		return nil, err
	}
	if occupied {
		t.sidelined = &SidelinedServer{Socket: remote, Reason: "existing endpoint is incompatible or cannot be inspected"}
		if info != nil {
			t.sidelined.Version = info.ServerVersion
			t.sidelined.Revision = info.ServerRevision
		}
		index := strings.LastIndexAny(remote, `/\`)
		side := remote[:index+1] + fmt.Sprintf("ssh-side-%016x.sock", stableID(remote+goprotocol.APISignature+fmt.Sprint(goprotocol.ProtocolVersion)))
		p.mu.Lock()
		p.remote = side
		p.mu.Unlock()
		t.remoteSocket = side
		// The old listener and its server remain untouched.
		if sideInfo, e := inspect(); e == nil {
			if !goprotocol.Assess(goclient.Descriptor(gobuild.Variant), sideInfo.Descriptor).Compatible {
				return nil, errors.New("side endpoint is incompatible")
			}
			t.warnings = p.diagnostics.String()
			ok = true
			return t, nil
		}
	}
	if err = p.start(); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		info, err = inspect()
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return nil, fmt.Errorf("remote server did not become ready: %w; SSH: %s", err, p.lastFailure())
	}
	if !goprotocol.Assess(goclient.Descriptor(gobuild.Variant), info.Descriptor).Compatible {
		return nil, errors.New("started remote server is incompatible")
	}
	t.warnings = p.diagnostics.String()
	ok = true
	return t, nil
}

// Used when recovering a portable server without an active tunnel.
func portableWaitForShutdown(destination, remote string, timeout time.Duration) error {
	p, err := newPortable(destination, nil)
	if err != nil {
		return err
	}
	defer p.close()
	p.remote = remote
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	for {
		occupied, err := p.occupied()
		if err != nil {
			return err
		}
		if !occupied {
			return nil
		}
		select {
		case <-ctx.Done():
			return errors.New("remote shutdown timed out; recovery retained")
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (p *portableTunnel) lastFailure() string {
	p.mu.Lock()
	diagnostic := p.lastDiagnostic
	p.mu.Unlock()
	if diagnostic == nil {
		return ""
	}
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	return string(diagnostic.text)
}

// Windows OpenSSH supplies scp/SFTP alongside ssh. SFTP transfers bytes without
// involving the login shell's stdin/encoding; publication verifies SHA256 first.
func (p *portableTunnel) upload(payload []byte, remote string) error {
	if p.native != nil {
		return p.native.upload(payload, remote)
	}
	file, err := os.CreateTemp("", "water-ssh-payload-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(payload); err != nil {
		file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	batch := "yes"
	if p.env != nil {
		batch = "no"
	}
	args := []string{"-q", "-o", "BatchMode=" + batch, "-o", "ConnectTimeout=10"}
	if runtime.GOOS != "windows" {
		args = append(args, "-o", "ControlPath="+controlMasterSocket(p.destination))
	}
	args = append(args, file.Name(), p.destination+":"+strings.ReplaceAll(remote, `\`, "/"))
	cmd := command("scp", args...)
	cmd.Env = p.env
	if _, err = p.diagnostics.output(cmd, 2*time.Minute); err != nil {
		return fmt.Errorf("upload Windows Water server via SFTP: %w", err)
	}
	return nil
}
