package goupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The native subprocess tests run the same headless installer binary logic;
// fake GUI programs provide a controlled startup acknowledgement.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == InstallerArgument {
		if err := RunInstaller(os.Args[2]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	if len(os.Args) == 3 && os.Args[1] == "--test-update-parent" {
		data, err := os.ReadFile(os.Args[2])
		if err != nil {
			os.Exit(2)
		}
		var p Plan
		if json.Unmarshal(data, &p) != nil {
			os.Exit(2)
		}
		p.ParentPID = os.Getpid()
		if StartInstaller(&p) != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNativeInstallerRestartAndRollback(t *testing.T) {
	for _, failStartup := range []bool{false, true} {
		t.Run(strconv.FormatBool(failStartup), func(t *testing.T) {
			dir := t.TempDir()
			stage := filepath.Join(dir, "stage")
			if err := os.Mkdir(stage, 0700); err != nil {
				t.Fatal(err)
			}
			pidFile := filepath.Join(dir, "gui.pid")
			statusFile := filepath.Join(dir, "status")
			gui := filepath.Join(dir, "water")
			newGUI := filepath.Join(stage, "new-water")
			oldScript := []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$WATER_TEST_UPDATE_PID\"\nprintf 'old:%s' \"$WATER_UPDATE_ERROR\" > \"$WATER_TEST_UPDATE_STATUS\"\nexec /usr/bin/tail -f /dev/null\n")
			newScript := []byte("#!/bin/sh\nprintf '%s' \"$$\" > \"$WATER_TEST_UPDATE_PID\"\nprintf ready > \"$WATER_UPDATE_ACK\"\nprintf new > \"$WATER_TEST_UPDATE_STATUS\"\nexec /usr/bin/tail -f /dev/null\n")
			if failStartup {
				newScript = []byte("#!/bin/sh\nexit 7\n")
			}
			if err := os.WriteFile(gui, oldScript, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(newGUI, newScript, 0700); err != nil {
				t.Fatal(err)
			}
			helper, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			p := Plan{Directory: stage, Helper: helper, Executable: gui, Entries: []Entry{{newGUI, gui}}}
			data, _ := json.Marshal(p)
			planPath := filepath.Join(dir, "parent-plan.json")
			if err = os.WriteFile(planPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			parent := exec.Command(helper, "--test-update-parent", planPath)
			parent.Env = append(os.Environ(), "WATER_TEST_UPDATE_PID="+pidFile, "WATER_TEST_UPDATE_STATUS="+statusFile)
			if err = parent.Run(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if data, e := os.ReadFile(pidFile); e == nil {
					if pid, e := strconv.Atoi(string(data)); e == nil && pid > 1 {
						_ = syscall.Kill(pid, syscall.SIGTERM)
					}
				}
				// Only the helper PID recorded in this test's private directory
				// may be cleaned if a failing test leaves it waiting.
				if data, e := os.ReadFile(filepath.Join(stage, "installer.pid")); e == nil {
					if pid, e := strconv.Atoi(string(data)); e == nil && pid > 1 {
						_ = syscall.Kill(pid, syscall.SIGTERM)
					}
				}
			}()
			deadline := time.NewTimer(8 * time.Second)
			defer deadline.Stop()
			ticker := time.NewTicker(20 * time.Millisecond)
			defer ticker.Stop()
			for {
				status, _ := os.ReadFile(statusFile)
				_, lockErr := os.Stat(filepath.Join(dir, ".water-update-lock"))
				if len(status) > 0 && os.IsNotExist(lockErr) {
					if failStartup {
						if !strings.HasPrefix(string(status), "old:new GUI exited") {
							t.Fatalf("rollback did not restart old GUI with error: %s", status)
						}
						contents, _ := os.ReadFile(gui)
						if !bytes.Equal(contents, oldScript) {
							t.Fatal("old executable was not restored")
						}
					} else {
						if string(status) != "new" {
							t.Fatalf("unexpected startup: %s", status)
						}
						if _, err := os.Stat(stage); !os.IsNotExist(err) {
							t.Fatal("successful staging directory was not cleaned")
						}
					}
					return
				}
				select {
				case <-deadline.C:
					t.Fatalf("installer did not finish, status=%s", status)
				case <-ticker.C:
				}
			}
		})
	}
}

func releaseFixture(tag, variant, platform, arch string) Release {
	prefix := "v"
	if variant == "dev" {
		prefix = "dev-"
	}
	version := strings.TrimPrefix(tag, prefix)
	name := strings.ReplaceAll(assetName(variant, version, platform, arch), " ", ".")
	return Release{Tag: tag, Prerelease: variant == "dev", Assets: []Asset{{Name: name, URL: "https://github.com/SurTeam/Water/releases/download/" + tag + "/" + name, Size: 20, Digest: "sha256:" + strings.Repeat("ab", 32)}}}
}

func TestSelectReleaseSeparatesVariantPlatformArchitectureAndTrust(t *testing.T) {
	stable := releaseFixture("v0.4.0", "release", "darwin", "arm64")
	dev := releaseFixture("dev-0.5.0", "dev", "darwin", "arm64")
	linux := releaseFixture("v0.6.0", "release", "linux", "amd64")
	for _, tc := range []struct{ variant, platform, arch, want string }{
		{"release", "darwin", "arm64", "0.4.0"}, {"dev", "darwin", "arm64", "0.5.0"}, {"release", "linux", "amd64", "0.6.0"}, {"release", "linux", "arm64", ""},
	} {
		c, err := selectRelease([]Release{dev, linux, stable}, "0.3.0", tc.variant, tc.platform, tc.arch)
		if err != nil {
			t.Fatal(err)
		}
		if tc.want == "" {
			if c != nil {
				t.Fatal("cross architecture update selected")
			}
			continue
		}
		if c == nil || c.Version != tc.want {
			t.Fatalf("%+v: got %+v", tc, c)
		}
	}
	for _, mutate := range []func(*Release){
		func(r *Release) { r.Draft = true }, func(r *Release) { r.Prerelease = true },
		func(r *Release) { r.Assets[0].Digest = "" }, func(r *Release) { r.Assets[0].Size = maxArchive + 1 },
		func(r *Release) { r.Assets[0].URL = "https://example.com/update.zip" },
		func(r *Release) { r.Assets[0].Name = "Water-0.4.0-macOS-arm64.dmg" },
	} {
		r := releaseFixture("v0.4.0", "release", "darwin", "arm64")
		mutate(&r)
		c, err := selectRelease([]Release{r}, "0.3.0", "release", "darwin", "arm64")
		if err != nil || c != nil {
			t.Fatalf("unsafe/incompatible release accepted: %+v %v", c, err)
		}
	}
	for _, current := range []string{"0.4.0", "0.5.0"} {
		c, err := selectRelease([]Release{stable}, current, "release", "darwin", "arm64")
		if err != nil || c != nil {
			t.Fatal("selected downgrade or same version")
		}
	}
}

func TestVersionPrecedence(t *testing.T) {
	versions := []string{"0.4.0-alpha", "0.4.0-alpha.1", "0.4.0-alpha.2", "0.4.0-alpha.10", "0.4.0-beta", "0.4.0", "0.10.0", "1.0.0"}
	for i := 1; i < len(versions); i++ {
		cmp, err := compareVersion(versions[i], versions[i-1])
		if err != nil || cmp <= 0 {
			t.Fatalf("wrong precedence %s %s", versions[i], versions[i-1])
		}
	}
	cmp, err := compareVersion("1.0.0+build.1", "1.0.0+build.2")
	if err != nil || cmp != 0 {
		t.Fatal("build metadata affected precedence")
	}
	for _, bad := range []string{"", "0.4", "v0.4.0", "0.04.0", "0.4.0/../../x", "0.4.0-", "0.4.0+../../x", "0.4.0+", "0.4.0-alpha.01"} {
		if _, _, err := versionParts(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestTimestampDevTagUsesAssetProductVersion(t *testing.T) {
	r := releaseFixture("dev-0.4.0", "dev", "darwin", "arm64")
	r.Tag = "dev-20261004-1200"
	r.Assets[0].URL = "https://github.com/SurTeam/Water/releases/download/" + r.Tag + "/" + r.Assets[0].Name
	c, err := selectRelease([]Release{r}, "0.3.0", "dev", "darwin", "arm64")
	if err != nil || c == nil || c.Version != "0.4.0" {
		t.Fatalf("timestamp dev update: %+v %v", c, err)
	}
}

func TestDownloadIntegrityAndCancellation(t *testing.T) {
	body := []byte("verified update contents")
	hash := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer server.Close()
	asset := Asset{URL: server.URL, Size: int64(len(body)), Digest: "sha256:" + hex.EncodeToString(hash[:])}
	var progress int64
	if err := download(context.Background(), server.Client(), asset, filepath.Join(t.TempDir(), "archive"), func(n int64) { progress = n }); err != nil || progress != asset.Size {
		t.Fatalf("valid download: %v %d", err, progress)
	}
	for _, mutate := range []func(*Asset){func(a *Asset) { a.Size++ }, func(a *Asset) { a.Size-- }, func(a *Asset) { a.Digest = "sha256:" + strings.Repeat("00", 32) }} {
		a := asset
		mutate(&a)
		if err := download(context.Background(), server.Client(), a, filepath.Join(t.TempDir(), "archive"), nil); err == nil {
			t.Fatal("accepted damaged download")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := download(ctx, server.Client(), asset, filepath.Join(t.TempDir(), "archive"), nil); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestFetchReleaseHTTPErrorsAndMalformedData(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{{429, "rate limited"}, {200, "invalid JSON"}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		_, err := fetchReleases(context.Background(), server.Client(), server.URL)
		server.Close()
		if err == nil {
			t.Fatal("ignored HTTP or JSON failure")
		}
	}
}

func TestUnpackRejectsTraversalAndSymlinks(t *testing.T) {
	for _, platform := range []string{"darwin", "linux"} {
		for _, name := range []string{"../outside", "/absolute", "link"} {
			var data bytes.Buffer
			if platform == "darwin" {
				writer := zip.NewWriter(&data)
				h := &zip.FileHeader{Name: name, Method: zip.Store}
				h.SetMode(0644)
				if name == "link" {
					h.SetMode(os.ModeSymlink | 0777)
				}
				w, err := writer.CreateHeader(h)
				if err != nil {
					t.Fatal(err)
				}
				_, _ = w.Write([]byte("bad"))
				_ = writer.Close()
			} else {
				z := gzip.NewWriter(&data)
				writer := tar.NewWriter(z)
				h := &tar.Header{Name: name, Mode: 0644, Size: 3, Typeflag: tar.TypeReg}
				if name == "link" {
					h.Typeflag = tar.TypeSymlink
					h.Linkname = "../outside"
					h.Size = 0
				}
				if err := writer.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
				if h.Size > 0 {
					_, _ = writer.Write([]byte("bad"))
				}
				_ = writer.Close()
				_ = z.Close()
			}
			dir := t.TempDir()
			archive := filepath.Join(dir, "archive")
			if err := os.WriteFile(archive, data.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			if err := unpack(archive, filepath.Join(dir, "extract"), platform); err == nil {
				t.Fatalf("accepted %s %s", platform, name)
			}
			if _, err := os.Stat(filepath.Join(dir, "outside")); !os.IsNotExist(err) {
				t.Fatal("wrote outside extraction directory")
			}
		}
	}
}

func TestReplacementRollbackAndSuccess(t *testing.T) {
	for _, failure := range []string{"second-file", "startup", "none"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			stage := filepath.Join(dir, "stage")
			_ = os.Mkdir(stage, 0700)
			plan := &Plan{Directory: stage}
			for _, name := range []string{"water", "water-server"} {
				target := filepath.Join(dir, name)
				source := filepath.Join(stage, "new-"+name)
				_ = os.WriteFile(target, []byte("old-"+name), 0700)
				_ = os.WriteFile(source, []byte("new-"+name), 0700)
				plan.Entries = append(plan.Entries, Entry{source, target})
			}
			if failure == "second-file" {
				_ = os.Remove(plan.Entries[1].Source)
			}
			err := replace(plan, func() error {
				if failure == "startup" {
					return errors.New("new GUI cannot attach")
				}
				return nil
			})
			if (err != nil) != (failure != "none") {
				t.Fatalf("unexpected result: %v", err)
			}
			for _, e := range plan.Entries {
				data, err := os.ReadFile(e.Target)
				if err != nil {
					t.Fatal(err)
				}
				prefix := "old-"
				if failure == "none" {
					prefix = "new-"
				}
				if string(data) != prefix+filepath.Base(e.Target) {
					t.Fatalf("incomplete transaction: %s", data)
				}
			}
		})
	}
}

func TestInstallationRequiresPackagedLocation(t *testing.T) {
	for _, path := range []string{"/tmp/water", "/Volumes/Water/Water.app/Contents/MacOS/water", "/private/var/AppTranslocation/id/Water.app/Contents/MacOS/water"} {
		if _, err := installation(path, "release", "darwin"); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	parent, err := installation("/Applications/Water Dev.app/Contents/MacOS/water-dev", "dev", "darwin")
	if err != nil || parent != "/Applications" {
		t.Fatalf("packaged dev: %s %v", parent, err)
	}
}

func TestPrepareLinuxPackageVerifiesAllIdentitiesAndCleansFailure(t *testing.T) {
	for _, wrongHelper := range []bool{false, true} {
		t.Run(strconv.FormatBool(wrongHelper), func(t *testing.T) {
			dir := t.TempDir()
			files := map[string]string{
				"water-dev":        "#!/bin/sh\nprintf '%s' '{\"build_variant\":\"dev\",\"client_version\":\"0.4.0\"}'\n",
				"water-srv-dev":    "#!/bin/sh\ncase \"$1\" in --build-variant) echo dev;; --version) echo 'water-server 0.4.0';; *) exit 1;; esac\n",
				"water-update-dev": "#!/bin/sh\nprintf '%s' '{\"build_variant\":\"dev\",\"version\":\"0.4.0\"}'\n",
			}
			if wrongHelper {
				files["water-update-dev"] = strings.ReplaceAll(files["water-update-dev"], "\"dev\"", "\"release\"")
			}
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			tw := tar.NewWriter(gz)
			for name, script := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("old "+name), 0700); err != nil {
					t.Fatal(err)
				}
				if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0755, Size: int64(len(script)), Typeflag: tar.TypeReg}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write([]byte(script)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := gz.Close(); err != nil {
				t.Fatal(err)
			}
			hash := sha256.Sum256(buf.Bytes())
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(buf.Bytes()) }))
			defer server.Close()
			c := Candidate{Version: "0.4.0", Asset: Asset{URL: server.URL, Size: int64(buf.Len()), Digest: "sha256:" + hex.EncodeToString(hash[:])}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			p, err := prepare(ctx, server.Client(), c, filepath.Join(dir, "water-dev"), "dev", "linux", []string{"--config", "preserved.json"}, nil)
			if wrongHelper {
				if err == nil || p != nil {
					t.Fatal("cross variant helper was accepted")
				}
				stages, _ := filepath.Glob(filepath.Join(dir, ".water-dev-update-*"))
				if len(stages) != 0 {
					t.Fatal("failed preparation left staging files")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if len(p.Entries) != 3 || p.Arguments[1] != "preserved.json" {
					t.Fatalf("incomplete install plan: %+v", p)
				}
				if err := replace(p, func() error { return errors.New("reject startup") }); err == nil {
					t.Fatal("ignored rejected startup")
				}
				_ = os.RemoveAll(p.Directory)
			}
			for name := range files {
				data, _ := os.ReadFile(filepath.Join(dir, name))
				if string(data) != "old "+name {
					t.Fatal("preparation or rollback changed the old package")
				}
			}
		})
	}
}
