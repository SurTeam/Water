package goremote

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func forwardSocket(destination, control, remote string) (*Tunnel, error) {
	local := filepath.Join(sshSocketDirectory(), "water-go-forward-"+uuid.NewString()+".sock")
	spec := local + ":" + remote
	if err := runSSH("-S", control, "-O", "forward", "-o", "ExitOnForwardFailure=yes", "-L", spec, destination); err != nil {
		return nil, err
	}
	return &Tunnel{destination: destination, localSocket: local, controlSocket: control, remoteSocket: remote, forwardSpec: spec}, nil
}

func legacyRemoteSockets(destination, control string) ([]string, error) {
	prefix := fmt.Sprintf("/tmp/water-go-%s-p", buildIdentityToken())
	suffix := fmt.Sprintf("-%016x.sock", stableID(destination))
	pattern := prefix + "*-*" + suffix
	out, err := boundedSSHOutput(command("ssh", "-S", control, "-o", "BatchMode=yes", destination, "for s in "+pattern+"; do if test -S \"$s\"; then printf '%s\\n' \"$s\"; fi; done"))
	if err != nil {
		return nil, fmt.Errorf("discover existing remote servers: %w", err)
	}
	paths := strings.Fields(string(out))
	if len(paths) > 16 {
		return nil, errors.New("too many legacy remote servers; specify WATER_REMOTE_CONTROL_SOCKET")
	}
	for _, p := range paths {
		if !strings.HasPrefix(p, prefix) || !strings.HasSuffix(p, suffix) || filepath.Clean(p) != p || strings.ContainsAny(strings.TrimPrefix(p, prefix), "/'\" \t\r\n;$`\\") {
			return nil, errors.New("invalid remote discovery response")
		}
	}
	return paths, nil
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
