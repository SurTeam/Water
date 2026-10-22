package goremote

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func forwardSocket(destination, control, remote string, collectors ...*sshDiagnostics) (*Tunnel, error) {
	d := diagnosticCollector(collectors)
	local := filepath.Join(sshSocketDirectory(), "water-go-forward-"+uuid.NewString()+".sock")
	spec := local + ":" + remote
	if err := d.run("-S", control, "-O", "forward", "-o", "ExitOnForwardFailure=yes", "-L", spec, destination); err != nil {
		return nil, err
	}
	return &Tunnel{destination: destination, localSocket: local, controlSocket: control, remoteSocket: remote, forwardSpec: spec}, nil
}

func legacyRemoteSockets(destination, control string, collectors ...*sshDiagnostics) ([]string, error) {
	d := diagnosticCollector(collectors)
	out, err := d.output(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, legacyDiscoveryCommand(destination)), startTimeout)
	if err != nil {
		return nil, fmt.Errorf("discover existing remote servers: %w", err)
	}
	paths := strings.Fields(string(out))
	if len(paths) > 16 {
		return nil, errors.New("too many legacy remote servers; specify WATER_REMOTE_CONTROL_SOCKET")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, legacySocketPrefix()) || !strings.HasSuffix(p, legacySocketSuffix(destination)) || filepath.Clean(p) != p || strings.ContainsAny(strings.TrimPrefix(p, legacySocketPrefix()), "/'\" \t\r\n;$`\\") {
			return nil, errors.New("invalid remote discovery response")
		}
	}
	return paths, nil
}

func legacySocketPrefix() string { return fmt.Sprintf("/tmp/water-go-%s-p", buildIdentityToken()) }

func legacySocketSuffix(destination string) string {
	return fmt.Sprintf("-%016x.sock", stableID(destination))
}

// legacyDiscoveryCommand runs inside the remote login shell, which may be zsh:
// an unmatched glob there aborts the shell with "no matches found" instead of
// being passed through like POSIX sh. Wrapping the loop in an explicit POSIX
// shell keeps discovery from failing when no legacy socket exists.
func legacyDiscoveryCommand(destination string) string {
	pattern := legacySocketPrefix() + "*-*" + legacySocketSuffix(destination)
	return "sh -c " + shellQuote("for s in "+pattern+"; do if test -S \"$s\"; then printf \"%s\\n\" \"$s\"; fi; done")
}

// AttachExisting creates a fresh forward when replacing a disconnected GUI
// view; cancelling the old forward cannot remove the replacement's endpoint.
func AttachExisting(destination, remote string) (*Tunnel, error) {
	control := controlMasterSocket(destination)
	if err := ensureControlMaster(destination, control); err != nil {
		return nil, err
	}
	return forwardSocket(destination, control, remote)
}

// StartReplacement keeps the discovered socket, including a migrated legacy
// socket, so other clients can find the restored workspace on reconnect.
func StartReplacement(destination, remote string) (*Tunnel, error) {
	control := controlMasterSocket(destination)
	if err := ensureControlMaster(destination, control); err != nil {
		return nil, err
	}
	t, err := forwardSocket(destination, control, remote)
	if err != nil {
		return nil, err
	}
	if err = t.Restart(); err != nil {
		_ = t.Close()
		return nil, err
	}
	return t, nil
}

func WaitForShutdown(destination, remote string, timeout time.Duration) error {
	control := controlMasterSocket(destination)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := boundedSSHOutput(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, "test ! -S "+shellQuote(remote)))
		if err == nil {
			return nil
		}
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			return fmt.Errorf("check remote shutdown: %w", err)
		}
		time.Sleep(retryInterval)
	}
	return errors.New("remote shutdown timed out; saved recovery remains available")
}

// Restart is called only after an acknowledged guarded recovery shutdown.
func (t *Tunnel) Restart() error {
	if err := startRemoteServer(t.destination, t.controlSocket, t.remoteSocket); err != nil {
		return err
	}
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		info, err := t.Client().Inspect(time.Second)
		if err == nil {
			if info.BuildVariant != gobuild.Variant || info.ServerRevision != gobuild.ServerRevision || info.ProtocolVersion != goprotocol.ProtocolVersion || info.APISignature != goprotocol.APISignature {
				return errors.New("replacement remote server does not match the bundled server")
			}
			return nil
		}
		time.Sleep(retryInterval)
	}
	return errors.New("replacement remote server did not become ready; saved recovery remains available")
}

// sideRemoteSocket is a second endpoint next to the stable one. It carries this
// build's protocol and API identity, so an incompatible server can keep its own
// endpoint untouched while a compatible server runs beside it.
func sideRemoteSocket(destination string) string {
	return filepath.Join(
		"/tmp",
		fmt.Sprintf(
			"water-go-%s-%016x-p%d-%08x.sock",
			buildIdentityToken(),
			stableID(destination),
			goprotocol.ProtocolVersion,
			stableID(goprotocol.APISignature)&0xffffffff,
		),
	)
}

// remoteSocketOccupied reports whether anything owns the remote endpoint. It
// distinguishes "no server yet" from "a server we cannot talk to", which decides
// between using the stable endpoint and starting beside an existing server.
func remoteSocketOccupied(destination, control, remote string, collectors ...*sshDiagnostics) (bool, error) {
	d := diagnosticCollector(collectors)
	out, err := d.output(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, "test -S "+shellQuote(remote)), startTimeout)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("inspect remote endpoint: %w", sshFailure(out, err))
}

// connectBeside starts this build on its own endpoint and keeps the incompatible
// server that owns the stable endpoint running.
func connectBeside(destination, control, primary string, preserved *goprotocol.ServerInfo, collectors ...*sshDiagnostics) (*Tunnel, error) {
	d := diagnosticCollector(collectors)
	side := sideRemoteSocket(destination)
	sidelined := &SidelinedServer{Socket: primary}
	if preserved != nil {
		sidelined.Version = preserved.ServerVersion
		sidelined.Revision = preserved.ServerRevision
		sidelined.Reason = goprotocol.Assess(goclient.Descriptor(gobuild.Variant), preserved.Descriptor).Reason
	}
	t, err := forwardSocket(destination, control, side, d)
	if err != nil {
		return nil, err
	}
	t.diagnostics = d
	t.sidelined = sidelined
	if info, err := t.Client().Inspect(time.Second); err == nil {
		if !goprotocol.Assess(goclient.Descriptor(gobuild.Variant), info.Descriptor).Compatible {
			_ = t.Close()
			return nil, fmt.Errorf("remote server at %s is incompatible with this build", side)
		}
		return t, nil
	}
	if err := startRemoteServer(destination, control, side, d); err != nil {
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
