package goremote

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateAskpassExchangeAndCleanup(t *testing.T) {
	env, cleanup, err := newAskpass(func(prompt string) (string, error) {
		if prompt != "Password:" {
			t.Errorf("unexpected prompt %q", prompt)
		}
		return "test-secret", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	var socket, helper string
	for _, entry := range env {
		if strings.HasPrefix(entry, "WATER_ASKPASS_SOCKET=") {
			socket = strings.TrimPrefix(entry, "WATER_ASKPASS_SOCKET=")
		}
		if strings.HasPrefix(entry, "SSH_ASKPASS=") {
			helper = strings.TrimPrefix(entry, "SSH_ASKPASS=")
		}
		if strings.Contains(entry, "test-secret") {
			t.Fatal("secret entered environment")
		}
	}
	for path, mode := range map[string]os.FileMode{filepath.Dir(socket): 0700, helper: 0700} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("%s mode %o", path, info.Mode().Perm())
		}
	}
	c, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err = json.NewEncoder(c).Encode("Password:"); err != nil {
		t.Fatal(err)
	}
	var reply askpassReply
	if err = json.NewDecoder(c).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if reply.Canceled || reply.Value != "test-secret" {
		t.Fatal("incorrect reply")
	}
	cleanup()
	if _, err = os.Stat(filepath.Dir(socket)); !os.IsNotExist(err) {
		t.Fatal("helper directory retained")
	}
}

func TestAuthenticationFallbackOnlyAfterRejection(t *testing.T) {
	for _, test := range []struct {
		name, message           string
		interactive, wantPrompt bool
	}{
		{"background", "Permission denied (publickey).", false, false},
		{"network", "Connection refused", true, false},
		{"hostkey", "Host key verification failed.", true, false},
		{"auth", "Permission denied (publickey).", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			script := filepath.Join(dir, "ssh")
			// A retry succeeds without invoking the helper; verify that it is configured.
			content := "#!/bin/sh\ncase \"$*\" in *'BatchMode=no'*) test \"$SSH_ASKPASS_REQUIRE\" = force || exit 3; echo 'Authentication warning' >&2; exit 0;; esac\necho " + shellQuote(test.message) + " >&2\nexit 255\n"
			if err := os.WriteFile(script, []byte(content), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("WATER_SSH_PROGRAM", script)
			var prompt Prompt
			if test.interactive {
				prompt = func(string) (string, error) { t.Fatal("fake ssh should not request a password"); return "", nil }
			}
			warning, err := ensureControlMasterInteractive("host", filepath.Join(dir, "master"), prompt)
			if test.wantPrompt {
				if err != nil || warning != "Authentication warning" {
					t.Fatalf("warning %q error %v", warning, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("missing original diagnostic: %v", err)
			}
		})
	}
}

func TestAuthenticationRejectionClassification(t *testing.T) {
	for _, message := range []string{"Permission denied (publickey).", "user@host: Permission denied (publickey,password).", "Warning before authentication\nuser@host: Permission denied (publickey,keyboard-interactive)."} {
		if !authenticationRejected(message) {
			t.Errorf("rejected authentication diagnostic %q", message)
		}
	}
	for _, message := range []string{"/home/user/key: Permission denied", "Permission denied", "Warning: permission denied while reading config", "remote: Permission denied (filesystem).", "Permission denied (publickey). malicious suffix"} {
		if authenticationRejected(message) {
			t.Errorf("accepted non-authentication failure %q", message)
		}
	}
}
