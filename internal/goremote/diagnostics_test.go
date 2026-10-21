package goremote

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAttemptDiagnosticsSeparateParsedOutputAndFailures(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "ssh")
	content := `#!/bin/sh
case "$*" in
 *'uname -s'*) printf 'Linux\nx86_64\n'; echo 'target warning' >&2;;
 *'for s in '*) echo 'discovery warning' >&2;;
 *'cat >'*) cat >/dev/null; echo 'deployment warning' >&2;;
 *'-O forward'*) echo 'forward warning' >&2;;
 *) echo 'final failure detail' >&2; exit 255;;
esac
`
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_PROGRAM", script)
	d := &sshDiagnostics{}
	target, err := detectRemoteTarget("host", "control", d)
	if err != nil || target.OS != "linux" || target.Arch != "amd64" {
		t.Fatalf("target parsing: %+v %v", target, err)
	}
	paths, err := legacyRemoteSockets("host", "control", d)
	if err != nil || len(paths) != 0 {
		t.Fatalf("discovery parsing: %v %v", paths, err)
	}
	if _, err = deployEmbeddedServer("host", "control", target, []byte("payload"), d); err != nil {
		t.Fatal(err)
	}
	if _, err = forwardSocket("host", "control", "remote", d); err != nil {
		t.Fatal(err)
	}
	for _, warning := range []string{"target warning", "discovery warning", "deployment warning", "forward warning"} {
		if !strings.Contains(d.String(), warning) {
			t.Fatalf("lost %s: %s", warning, d.String())
		}
	}
	if err = d.run("failure"); err == nil || !strings.Contains(err.Error(), "final failure detail") {
		t.Fatalf("missing failure stderr: %v", err)
	}
	if other := (&sshDiagnostics{}).String(); other != "" {
		t.Fatal("diagnostics leaked across attempts")
	}
}

func TestConnectRetainsEarlierWarningsOnFailure(t *testing.T) {
	script := filepath.Join(t.TempDir(), "ssh")
	content := "#!/bin/sh\ncase \"$*\" in *'-O check'*) echo 'existing master warning' >&2;exit 0;; *'-O forward'*) echo 'forward denied' >&2;exit 255;; esac\nexit 1\n"
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_PROGRAM", script)
	_, err := Connect("test-host")
	if err == nil || !strings.Contains(err.Error(), "existing master warning") || !strings.Contains(err.Error(), "forward denied") {
		t.Fatalf("missing accumulated diagnostics: %v", err)
	}
}
