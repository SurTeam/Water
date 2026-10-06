package goserver_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/gobuild"
	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/google/uuid"
)

func recoveryServer(t *testing.T, cfg goconfig.AppConfig, configure ...func(*goserver.Server)) (*goserver.Server, *goclient.Client) {
	t.Helper()
	socket := filepath.Join("/tmp", "water-recovery-"+uuid.NewString()+".sock")
	s := goserver.NewWithConfig(socket, cfg)
	for _, init := range configure {
		init(s)
	}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("owned server did not exit")
		}
		_ = os.Remove(socket + ".lock")
	})
	c := goclient.New(socket)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := c.Inspect(100 * time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("owned server not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Logf("owned server instance=%s socket=%s", s.InstanceID, socket)
	return s, c
}
func recoveryConfig(t *testing.T) goconfig.AppConfig {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := goconfig.Default()
	cfg.Startup.DefaultCWD = &dir
	cfg.Shell.Program = "/bin/sh"
	cfg.Shell.Args = []string{"-i"}
	return cfg
}
func openRecoverySession(t *testing.T, c *goclient.Client) *goclient.Session {
	t.Helper()
	s, err := c.OpenSession()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecoveryRPCPreservesLiveDirectoriesAndTopology(t *testing.T) {
	cfg := recoveryConfig(t)
	_, source := recoveryServer(t, cfg)
	session := openRecoverySession(t, source)
	if err := session.Dispatch(map[string]any{"type": "workspace.new"}, nil); err != nil {
		t.Fatal(err)
	}
	first := stateDump(t, source)
	pane := *first.FocusedPane
	if err := session.Dispatch(map[string]any{"type": "pane.split", "pane_id": pane, "direction": "right"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "pane.split", "pane_id": pane, "direction": "down"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "pane.resize_split", "tab_id": first.Workspaces[0].Tabs[0].ID, "path": []bool{}, "ratio": .27}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "workspace.new"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "workspace.rename", "workspace_id": first.Workspaces[0].ID, "title": "Kept workspace"}, nil); err != nil {
		t.Fatal(err)
	}
	changed := filepath.Join(cfg.DefaultCWD(), "after-cd")
	if err := os.Mkdir(changed, 0700); err != nil {
		t.Fatal(err)
	}
	if err := session.Dispatch(map[string]any{"type": "terminal.send_text", "pane_id": pane, "text": "cd '" + changed + "'; printf 'RECOVERY_%s\\n' 'CWD_READY'\n"}, nil); err != nil {
		t.Fatal(err)
	}
	// Obtain terminal ID from state, but wait for actual command output rather
	// than accepting the echoed input as evidence of a completed cd.
	var tree map[string]any
	_ = json.Unmarshal(first.Workspaces[0].Tabs[0].Tree, &tree)
	surface := tree["surface_state"].(map[string]any)["Terminal"].(map[string]any)
	terminal := surface["terminal_id"]
	if err := session.Call("terminal.contains", map[string]any{"terminal_id": terminal, "text": "RECOVERY_CWD_READY", "timeout_ms": 3000}, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "saved.json")
	p, err := goclient.PrepareRecovery(session, path, gobuild.Variant, "owned-source", true)
	if err != nil {
		t.Fatal(err)
	}
	var nodes []*gomodel.RecoveryNode
	var walk func(*gomodel.RecoveryNode)
	walk = func(n *gomodel.RecoveryNode) {
		if n.PaneID != uuid.Nil {
			nodes = append(nodes, n)
			return
		}
		walk(n.First)
		walk(n.Second)
	}
	for _, w := range p.Layout.Workspaces {
		for _, tab := range w.Tabs {
			walk(tab.Tree)
		}
	}
	found := false
	for _, n := range nodes {
		if n.PaneID == pane {
			found = true
			if n.CWD != changed {
				t.Fatalf("saved cwd=%q want %q", n.CWD, changed)
			}
		}
	}
	if !found {
		t.Fatal("pane missing from export")
	}
	if err := session.Dispatch(map[string]any{"type": "workspace.create"}, nil); err == nil {
		t.Fatal("model mutation allowed during recovery lease")
	}
	if _, err := source.OpenSession(); err == nil {
		t.Fatal("new window admitted during restart")
	}
	if err := session.Call("recovery.cancel", map[string]any{"token": p.Token}, nil); err != nil {
		t.Fatal(err)
	}
	_, target := recoveryServer(t, cfg)
	replacement := openRecoverySession(t, target)
	// Fail after at least one shell is prepared: rollback must remove all PTYs
	// and leave no partial layout, so retry can succeed using the same artifact.
	raw, _ := json.Marshal(p.Layout)
	var invalid gomodel.RecoveryLayout
	_ = json.Unmarshal(raw, &invalid)
	invalid.Workspaces[1].Tabs[0].Tree.CWD = filepath.Join(changed, "missing")
	if err := replacement.Dispatch(map[string]any{"type": "recovery.restore", "layout": invalid}, nil); err == nil {
		t.Fatal("missing directory accepted")
	}
	if len(stateDump(t, target).Workspaces) != 0 {
		t.Fatal("failed restore left partial topology")
	}
	var memory struct {
		Terminals int `json:"terminal_count"`
	}
	if err := target.Call("debug.memory", nil, &memory); err != nil || memory.Terminals != 0 {
		t.Fatalf("rollback leaked PTYs: %+v err=%v", memory, err)
	}
	if err := goclient.RestorePending(replacement, path, gobuild.Variant, "owned-source"); err != nil {
		t.Fatal(err)
	}
	var exported goclient.RecoveryPrepared
	if err := replacement.Call("recovery.prepare", nil, &exported); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recoveryTopology(p.Layout), recoveryTopology(exported.Layout)) || !reflect.DeepEqual(p.Layout.ActiveWorkspace, exported.Layout.ActiveWorkspace) {
		t.Fatalf("restored layout differs\nbefore=%+v\nafter=%+v", p.Layout, exported.Layout)
	}
	_ = replacement.Call("recovery.cancel", map[string]any{"token": exported.Token}, nil)
	if err := replacement.Dispatch(map[string]any{"type": "recovery.restore", "layout": p.Layout}, nil); err != nil {
		t.Fatalf("idempotent retry failed: %v", err)
	}
	if len(stateDump(t, target).Workspaces) != 2 {
		t.Fatal("retry duplicated workspaces")
	}
	saved, err := goclient.LoadRecovery(path, gobuild.Variant, "owned-source")
	if err != nil || saved.Pending {
		t.Fatalf("completion not recorded: %+v err=%v", saved, err)
	}
}

func recoveryTopology(d gomodel.RecoveryLayout) []gomodel.RecoveryWorkspace {
	raw, _ := json.Marshal(d)
	var copy gomodel.RecoveryLayout
	_ = json.Unmarshal(raw, &copy)
	var strip func(*gomodel.RecoveryNode)
	strip = func(n *gomodel.RecoveryNode) {
		if n.PaneID != uuid.Nil {
			if n.Terminal {
				n.SurfaceID = uuid.Nil
			}
			return
		}
		strip(n.First)
		strip(n.Second)
	}
	for _, w := range copy.Workspaces {
		for _, tab := range w.Tabs {
			strip(tab.Tree)
		}
	}
	return copy.Workspaces
}

func TestRecoveryRefusesOtherWindowsAndFailedSaveLeavesServerRunning(t *testing.T) {
	cfg := recoveryConfig(t)
	_, client := recoveryServer(t, cfg)
	one := openRecoverySession(t, client)
	two := openRecoverySession(t, client)
	if err := one.Call("recovery.prepare", nil, nil); err == nil {
		t.Fatal("restart allowed with another window")
	}
	_ = two.Close()
	deadline := time.Now().Add(time.Second)
	for {
		info, err := client.Inspect(time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if info.UISessions == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("closed GUI session retained")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// A directory cannot be replaced by the atomic recovery file rename.
	if _, err := goclient.PrepareRecovery(one, t.TempDir(), gobuild.Variant, "owned", true); err == nil {
		t.Fatal("invalid save path accepted")
	}
	if err := one.Dispatch(map[string]any{"type": "workspace.create"}, nil); err != nil {
		t.Fatalf("failed save stopped/froze server: %v", err)
	}
}

func TestProductUpgradeReusesSupportedServer(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprintf("server-changed-%v", changed), func(t *testing.T) {
			_, client := recoveryServer(t, recoveryConfig(t), func(s *goserver.Server) {
				s.Version = "older-product-version"
				if changed {
					s.Revision = "older-server-revision"
				}
			})
			session := openRecoverySession(t, client)
			if session.Diagnostic || !session.Compatibility.Compatible || session.Compatibility.RestartRecommended != changed {
				t.Fatalf("unexpected admission: %+v", session.Compatibility)
			}
			if session.Server.ServerVersion != "older-product-version" || session.Server.InstanceID == "" {
				t.Fatal("server metadata lost")
			}
		})
	}
}

func TestMetadataAndBilateralAdmissionBeforeModelOperations(t *testing.T) {
	_, client := recoveryServer(t, recoveryConfig(t))
	conn, err := net.Dial("unix", client.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	exchange := func(id uint64, protocol uint32, method string, params any) goprotocol.WireMessage {
		t.Helper()
		raw, _ := json.Marshal(params)
		if err := goprotocol.WriteJSON(conn, goprotocol.WireMessage{BuildVariant: gobuild.Variant, ProtocolVersion: protocol, RequestID: id, Method: method, Params: raw}); err != nil {
			t.Fatal(err)
		}
		frame, err := goprotocol.ReadFrame(conn)
		if err != nil {
			t.Fatal(err)
		}
		var response goprotocol.WireMessage
		if err := json.Unmarshal(frame.JSON, &response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	inspected := exchange(1, goprotocol.ProtocolVersion+1, "server.inspect", nil)
	if inspected.OK == nil || !*inspected.OK {
		t.Fatalf("metadata probe blocked: %+v", inspected)
	}
	descriptor := goclient.Descriptor(gobuild.Variant)
	descriptor.Capabilities = []string{"terminal-stream/v1"}
	opened := exchange(2, goprotocol.ProtocolVersion, "session.open", map[string]any{"role": "gui", "client": descriptor})
	var admission struct {
		Diagnostic    bool                     `json:"diagnostic"`
		Compatibility goprotocol.Compatibility `json:"compatibility"`
	}
	_ = json.Unmarshal(opened.Result, &admission)
	if !admission.Diagnostic || admission.Compatibility.Compatible || len(admission.Compatibility.MissingClient) != 1 {
		t.Fatalf("server failed bilateral check: %+v", admission)
	}
	before := stateDump(t, client).StateRevision
	blocked := exchange(3, goprotocol.ProtocolVersion, "command.dispatch", map[string]any{"command": map[string]any{"type": "workspace.create"}})
	if blocked.OK == nil || *blocked.OK || stateDump(t, client).StateRevision != before {
		t.Fatal("diagnostic session mutated model")
	}
}
