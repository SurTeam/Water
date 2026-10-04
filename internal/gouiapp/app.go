package gouiapp

import (
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hajimehoshi/ebiten/v2"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/goremote"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goui"
	"github.com/SurTeam/Water/internal/goupdate"
)

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("SSH destination must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func Run(arguments []string, buildVariant string) error {
	var socket string
	var configPath string
	var sshDestinations stringListFlag
	flags := flag.NewFlagSet("water", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	flags.StringVar(&socket, "socket", "", "Unix control socket (compatibility alias)")
	flags.StringVar(&socket, "control-socket", "", "Unix control socket")
	flags.StringVar(&configPath, "config", goconfig.DefaultLoadPath(buildVariant), "Water config JSON")
	flags.Var(&sshDestinations, "ssh", "SSH destination to add to this window (may be repeated)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}

	cfg, err := goconfig.Load(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	socket = resolveGUISocket(socket, cfg, buildVariant)

	return runWindowWithConnections(socket, configPath, cfg, buildVariant, sshDestinations)
}

func runWindow(socket, configPath string, cfg goconfig.AppConfig, buildVariant string) error {
	return runWindowWithConnections(socket, configPath, cfg, buildVariant, nil)
}

func runWindowWithConnection(socket, configPath string, cfg goconfig.AppConfig, buildVariant, remoteDestination string) error {
	var destinations []string
	if strings.TrimSpace(remoteDestination) != "" {
		destinations = []string{remoteDestination}
	}
	return runWindowWithConnections(socket, configPath, cfg, buildVariant, destinations)
}

func runWindowWithConnections(socket, configPath string, cfg goconfig.AppConfig, buildVariant string, sshDestinations []string) error {
	var w *goui.EbitengineWindow
	invalidate := func() {
		if w != nil {
			w.Invalidate()
		}
	}
	multi := goui.NewMultiWorkspaceClient(invalidate)
	w = goui.NewEbitengineWindow(multi, cfg)
	restartArgs := []string{"--control-socket", socket, "--config", configPath}
	for _, destination := range sshDestinations {
		restartArgs = append(restartArgs, "--ssh", destination)
	}
	updates := goupdate.NewManager(gobuild.Version, buildVariant, restartArgs, invalidate)
	defer updates.Close()
	w.SetUpdateManager(updates)
	w.SetNewWindowHandler(func() error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		args := []string{"--control-socket", socket, "--config", configPath}
		for _, destination := range sshDestinations {
			args = append(args, "--ssh", destination)
		}
		command := exec.Command(executable, args...)
		command.Env = os.Environ()
		if err := command.Start(); err != nil {
			return err
		}
		go func() { _ = command.Wait() }()
		return nil
	})
	defer w.Close()
	settingsStore := goui.NewSettingsStore(cfg)

	localSession, embedded, _, localErr := connectOrStart(socket, configPath, cfg, buildVariant)
	w.SetUpdateRestartAllowed(embedded == nil)
	if localErr == nil {
		w.SetQuitServerHandler(func() error {
			return localSession.CallTimeout("server.shutdown", map[string]any{}, nil, 5*time.Second)
		})
		if embedded != nil {
			defer func() { embedded.WaitForGUIRelease(); _ = embedded.Close() }()
		}
		localView := goui.NewWorkspaceClientWithConnection(localSession, invalidate, cfg, "")
		w.Attach(localView)
		localView.SetConfigPath(configPath)
		localView.SetSettingsStore(settingsStore)
		if err := multi.AddConnection(goui.ConnectionEntry{
			ID:         uuid.New(),
			Name:       "Local",
			Kind:       "local",
			Status:     "connected",
			SocketPath: socket,
		}, localView, func() {
			if embedded == nil && !cfg.Server.DetachOnQuit && !w.Updating() {
				_ = localSession.CallTimeout("session.release", map[string]any{"shutdown_if_last": true}, nil, time.Second)
			}
			_ = localSession.Close()
		}, len(sshDestinations) == 0); err != nil {
			return err
		}
	} else if len(sshDestinations) == 0 {
		return localErr
	}

	// Register this after local server lifecycle defers so child sessions and
	// terminal attachments are closed before an embedded/detached server stops.
	defer multi.Close()

	remoteFactory := func(rawDestination string) (goui.ConnectionEntry, *goui.WorkspaceClient, func(), error) {
		destination, err := goremote.ValidateDestination(rawDestination)
		if err != nil {
			return goui.ConnectionEntry{}, nil, nil, err
		}
		tunnel, err := goremote.Connect(destination)
		if err != nil {
			return goui.ConnectionEntry{}, nil, nil, fmt.Errorf("connect remote Water server %s: %w", destination, err)
		}
		session, err := tunnel.Client().OpenSession()
		if err != nil {
			_ = tunnel.Close()
			return goui.ConnectionEntry{}, nil, nil, fmt.Errorf("open remote Water session %s: %w", destination, err)
		}
		view := goui.NewWorkspaceClientWithConnection(session, invalidate, cfg, destination)
		w.Attach(view)
		view.SetConfigPath(configPath)
		view.SetSettingsStore(settingsStore)
		entry := goui.ConnectionEntry{
			ID:               uuid.New(),
			Name:             destination,
			Kind:             "remote",
			Status:           "connected",
			SocketPath:       tunnel.LocalSocket(),
			RemoteSocketPath: tunnel.RemoteSocket(),
			Destination:      destination,
		}
		closeRemote := func() {
			_ = session.Close()
			_ = tunnel.Close()
		}
		return entry, view, closeRemote, nil
	}
	multi.SetRemoteConnector(remoteFactory)

	for _, rawDestination := range sshDestinations {
		entry, view, closeRemote, err := remoteFactory(rawDestination)
		if err != nil {
			return fmt.Errorf("SSH destination %q: %w", rawDestination, err)
		}
		if err := multi.AddConnection(entry, view, closeRemote, true); err != nil {
			if closeRemote != nil {
				closeRemote()
			}
			return err
		}
	}
	if len(multi.ConnectionEntries()) == 0 {
		if localErr != nil {
			return localErr
		}
		return fmt.Errorf("no Water connection is available")
	}

	if err := multi.Bootstrap(); err != nil {
		return err
	}
	multi.Run()

	ebiten.SetWindowTitle("Water")
	ebiten.SetWindowDecorated(false)
	initialSize, minimumSize := w.InitialWindowSize(), w.MinimumWindowSize()
	ebiten.SetWindowSize(initialSize.X, initialSize.Y)
	ebiten.SetWindowSizeLimits(minimumSize.X, minimumSize.Y, -1, -1)
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)
	ebiten.SetWindowClosingHandled(true)
	ebiten.SetRunnableOnUnfocused(true)
	// Hidden windows keep Update running to service control and terminal input.
	// Draw owns clearing so an invisible frame submits no graphics commands.
	ebiten.SetScreenClearedEveryFrame(false)
	// Retaining the screen alone still runs Update at 60 Hz. Minimum FPS mode
	// sleeps until native input or the window's bounded scheduler wakes it.
	ebiten.SetFPSMode(ebiten.FPSModeVsyncOffMinimum)
	// Update exactly once before each presentation, including minimum mode;
	// text events must not accumulate while fixed-tick catch-up skips an Update.
	ebiten.SetTPS(ebiten.SyncWithFPS)
	return ebiten.RunGameWithOptions(w, &ebiten.RunGameOptions{ScreenTransparent: true, X11ClassName: "Water", X11InstanceName: "water-" + buildVariant})
}

func connectOrStart(socket, configPath string, cfg goconfig.AppConfig, buildVariant string) (*goclient.Session, *goserver.Server, bool, error) {
	client := goclient.New(socket)
	if session, err := client.OpenSession(); err == nil {
		return session, nil, false, nil
	}
	if conn, err := net.DialTimeout("unix", socket, 250*time.Millisecond); err == nil {
		_ = conn.Close()
		return nil, nil, false, fmt.Errorf("control socket belongs to an incompatible server: %s", socket)
	}
	if !cfg.Server.AutoStart {
		return nil, nil, false, fmt.Errorf("server is not available at %s and auto_start is disabled", socket)
	}

	if !cfg.Server.Detached {
		srv := goserver.NewWithConfig(socket, cfg)
		if err := srv.Initialize(cfg.Startup.InitialWorkspace, cfg.Startup.InitialTerminal && cfg.Startup.InitialWorkspace); err != nil {
			return nil, nil, false, err
		}
		errCh := make(chan error, 1)
		go func() { errCh <- srv.ListenAndServe() }()
		session, err := waitForSession(socket, 3*time.Second)
		if err != nil {
			_ = srv.Close()
			select {
			case serveErr := <-errCh:
				if serveErr != nil {
					return nil, nil, false, serveErr
				}
			default:
			}
			return nil, nil, false, err
		}
		select {
		case <-errCh:
			_ = srv.Close()
			return session, nil, false, nil
		default:
		}
		return session, srv, false, nil
	}

	if err := startDetachedServer(socket, configPath, buildVariant); err != nil {
		if session, connectErr := waitForSession(socket, 5*time.Second); connectErr == nil {
			return session, nil, false, nil
		}
		return nil, nil, false, err
	}
	session, err := waitForSession(socket, 5*time.Second)
	if err != nil {
		return nil, nil, false, err
	}
	return session, nil, true, nil
}

func waitForSession(socket string, timeout time.Duration) (*goclient.Session, error) {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		session, err := goclient.New(socket).OpenSession()
		if err == nil {
			return session, nil
		}
		last = err
		time.Sleep(50 * time.Millisecond)
	}
	if last == nil {
		last = fmt.Errorf("timed out")
	}
	return nil, fmt.Errorf("server at %s did not become ready: %w", socket, last)
}

func startDetachedServer(socket, configPath, buildVariant string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	candidates := []string{filepath.Join(dir, "water-server"), filepath.Join(dir, "water-srv-dev")}
	if buildVariant != "release" {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}
	serverPath := ""
	for _, candidate := range candidates {
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			serverPath = candidate
			break
		}
	}
	if serverPath == "" {
		if found, lookErr := exec.LookPath("water-server"); lookErr == nil {
			serverPath = found
		} else {
			return fmt.Errorf("water-server executable not found next to %s or in PATH", exe)
		}
	}
	variantOut, err := exec.Command(serverPath, "--build-variant").Output()
	if err != nil {
		return fmt.Errorf("inspect sibling water-server: %w", err)
	}
	if got := strings.TrimSpace(string(variantOut)); got != buildVariant {
		return fmt.Errorf("sibling water-server variant %q does not match GUI variant %q", got, buildVariant)
	}
	cmd := exec.Command(
		serverPath,
		"--control-socket", socket,
		"--config", configPath,
		"--daemonize",
	)
	// Older Water servers leaked their private daemon marker into PTY shells.
	// A GUI launched from one of those shells must still start a fresh launcher.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "WATER_GO_DAEMON_CHILD=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("could not start water-server: %s", message)
	}
	return nil
}

func resolveGUISocket(explicit string, cfg goconfig.AppConfig, buildVariant string) string {
	if explicit != "" {
		return explicit
	}
	if p := os.Getenv("WATER_CONTROL_SOCKET"); p != "" {
		return p
	}
	if p := os.Getenv("WATER_SOCKET"); p != "" {
		return p
	}
	if cfg.Server.SocketPath != nil && strings.TrimSpace(*cfg.Server.SocketPath) != "" {
		return strings.TrimSpace(*cfg.Server.SocketPath)
	}
	if cfg.Startup.ControlSocket != nil && strings.TrimSpace(*cfg.Startup.ControlSocket) != "" {
		return strings.TrimSpace(*cfg.Startup.ControlSocket)
	}
	if buildVariant == "release" {
		return "/tmp/water.sock"
	}
	return "/tmp/water-dev.sock"
}
