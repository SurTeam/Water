package gouiapp

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/SurTeam/Water/internal/goui"
)

type serverOperations struct {
	configPath, variant string
	cfg                 goconfig.AppConfig
	window              *goui.EbitengineWindow
	settings            *goui.SettingsStore
	multi               *goui.MultiWorkspaceClient
}

func recoveryEndpoint(entry goui.ConnectionEntry) string {
	if entry.Kind == "remote" {
		return "ssh:" + entry.Destination
	}
	return "local:" + entry.SocketPath
}
func (o *serverOperations) recoveryPath(entry goui.ConnectionEntry) string {
	return goclient.RecoveryPath(o.configPath, o.variant, recoveryEndpoint(entry))
}
func (o *serverOperations) newView(s *goclient.Session, entry goui.ConnectionEntry) *goui.WorkspaceClient {
	if entry.Kind == "local" && s.Server.ServerPID > 0 {
		o.window.SetUpdateRestartAllowed(s.Server.ServerPID != os.Getpid())
	}
	c := goui.NewWorkspaceClientWithConnection(s, o.window.Invalidate, o.cfg, entry.Destination)
	o.window.Attach(c)
	c.SetConfigPath(o.configPath)
	c.SetSettingsStore(o.settings)
	path := o.recoveryPath(entry)
	if err := goclient.RestorePending(s, path, o.variant, recoveryEndpoint(entry)); err != nil {
		c.SetServerRecoveryError(path, err)
	}
	return c
}
func (o *serverOperations) operate(entry goui.ConnectionEntry, session *goclient.Session, action string) (goui.ConnectionEntry, *goui.WorkspaceClient, func(), string, error) {
	path := o.recoveryPath(entry)
	endpoint := recoveryEndpoint(entry)
	fail := func(err error) (goui.ConnectionEntry, *goui.WorkspaceClient, func(), string, error) {
		return entry, nil, nil, path, err
	}
	if o.window.Updating() {
		return fail(errors.New("software update is in progress; server was left running"))
	}
	project := func(d *gomodel.RecoveryLayout) { o.multi.ProjectRecoveryLayout(entry.ID, d) }
	if action == "backup" {
		_, err := goclient.PrepareRecovery(session, path, o.variant, endpoint, false, project)
		return fail(err)
	}
	if action != "restart" && action != "restore" {
		return fail(errors.New("unknown server operation"))
	}
	if action == "restart" {
		if entry.Kind == "local" {
			executable, err := os.Executable()
			if err != nil {
				return fail(err)
			}
			if _, err := resolveSiblingServer(executable, o.variant); err != nil {
				return fail(fmt.Errorf("replacement preflight; server left running: %w", err))
			}
		}
		if !goprotocol.HasCapability(session.Server.Capabilities, "recovery/v1") {
			return fail(errors.New("legacy server cannot safely export current directories; automatic restart is unavailable"))
		}
		p, err := goclient.PrepareRecovery(session, path, o.variant, endpoint, true, project)
		if err != nil {
			return fail(err)
		}
		if err := session.CallTimeout("recovery.shutdown", map[string]any{"token": p.Token}, nil, 3*time.Second); err != nil {
			_ = session.CallTimeout("recovery.cancel", map[string]any{"token": p.Token}, nil, time.Second)
			return fail(err)
		}
		if entry.Kind == "remote" {
			err = goremote.WaitForShutdown(entry.Destination, entry.RemoteSocketPath, 5*time.Second)
		} else {
			err = goclient.WaitForShutdown(goclient.New(entry.SocketPath), p.Layout.SourceInstance, 5*time.Second)
		}
		if err != nil {
			return fail(err)
		}
	} else {
		a, err := goclient.LoadRecovery(path, o.variant, endpoint)
		if err != nil {
			return fail(err)
		}
		if !a.Pending {
			return fail(errors.New("no pending recovery; saved backup remains available"))
		}
		if info, err := goclient.New(entry.SocketPath).Inspect(time.Second); err == nil {
			if info.InstanceID == a.Layout.SourceInstance {
				return fail(errors.New("original server is still running; use Restart server to save and restart"))
			}
			var existingTunnel *goremote.Tunnel
			if entry.Kind == "remote" {
				existingTunnel, err = goremote.AttachExisting(entry.Destination, entry.RemoteSocketPath)
				if err != nil {
					return fail(err)
				}
				entry.SocketPath = existingTunnel.LocalSocket()
			}
			s, err := goclient.New(entry.SocketPath).OpenSession()
			if err != nil {
				if existingTunnel != nil {
					_ = existingTunnel.Close()
				}
				return fail(err)
			}
			if err := goclient.RestorePending(s, path, o.variant, endpoint); err != nil {
				_ = s.Close()
				if existingTunnel != nil {
					_ = existingTunnel.Close()
				}
				return fail(err)
			}
			view := o.newView(s, entry)
			return entry, view, o.closeSession(s, entry.Kind == "local", existingTunnel), path, nil
		}
	}
	var s *goclient.Session
	var tunnel *goremote.Tunnel
	var err error
	if entry.Kind == "remote" {
		tunnel, err = goremote.StartReplacement(entry.Destination, entry.RemoteSocketPath)
		if err == nil {
			entry.SocketPath = tunnel.LocalSocket()
			s, err = tunnel.Client().OpenSession()
		}
	} else {
		cfg, loadErr := goconfig.Load(o.configPath)
		if loadErr != nil {
			return fail(loadErr)
		}
		cfg.Server.Detached = true
		s, _, _, err = connectOrStartWithRecovery(entry.SocketPath, o.configPath, cfg, o.variant, true)
	}
	if err != nil {
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return fail(fmt.Errorf("start replacement; recovery saved at %s: %w", path, err))
	}
	cleanup := o.closeSession(s, entry.Kind == "local", tunnel)
	if err := goclient.RestorePending(s, path, o.variant, endpoint); err != nil {
		_ = s.Close()
		if tunnel != nil {
			_ = tunnel.Close()
		}
		return fail(err)
	}
	return entry, o.newView(s, entry), cleanup, path, nil
}
func (o *serverOperations) closeSession(s *goclient.Session, local bool, tunnel *goremote.Tunnel) func() {
	detachOnQuit := o.cfg.Server.DetachOnQuit
	if local {
		if cfg, err := goconfig.Load(o.configPath); err == nil {
			detachOnQuit = cfg.Server.DetachOnQuit
		} else {
			detachOnQuit = true
		}
	}
	return func() {
		if local && !detachOnQuit && !o.window.Updating() {
			_ = s.CallTimeout("session.release", map[string]any{"shutdown_if_last": true}, nil, time.Second)
		}
		_ = s.Close()
		if tunnel != nil {
			_ = tunnel.Close()
		}
	}
}

func pendingRecovery(configPath, variant string, entry goui.ConnectionEntry) bool {
	a, err := goclient.LoadRecovery(goclient.RecoveryPath(configPath, variant, recoveryEndpoint(entry)), variant, recoveryEndpoint(entry))
	return err != nil && !errors.Is(err, os.ErrNotExist) || a.Pending
}
