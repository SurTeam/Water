//go:build darwin

package goupdate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A real LaunchServices bundle verifies both environment forwarding and that
// open -W tracks the application lifetime rather than just request acceptance.
func TestDarwinOpenWaitBundle(t *testing.T) {
	dir := t.TempDir()
	bundle := filepath.Join(dir, "WaterUpdateFixture.app")
	mac := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(mac, 0700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>fixture</string><key>CFBundleIdentifier</key><string>org.water.updatefixture</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0600)
	source := filepath.Join(dir, "fixture.c")
	os.WriteFile(source, []byte(`#include <stdio.h>
#include <stdlib.h>
#include <unistd.h>
int main(){const char *p=getenv("WATER_FIXTURE_PID");if(!p)return 9;FILE*f=fopen(p,"w");if(!f)return 8;fprintf(f,"%d",getpid());fclose(f);sleep(2);return 0;}`), 0600)
	if out, err := exec.Command("/usr/bin/clang", source, "-o", filepath.Join(mac, "fixture")).CombinedOutput(); err != nil {
		t.Fatalf("compile fixture: %v: %s", err, out)
	}
	marker := filepath.Join(dir, "pid")
	cmd := exec.Command("/usr/bin/open", "-n", "-W", "--env", "WATER_FIXTURE_PID="+marker, bundle, "--args", "fixture")
	start := time.Now()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if b, e := os.ReadFile(marker); e == nil {
			if pid, e := strconv.Atoi(strings.TrimSpace(string(b))); e == nil && pid > 1 {
				p, _ := os.FindProcess(pid)
				_ = p.Kill()
			}
		}
	}()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	limit := time.After(10 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("open exited before application started: %v", err)
		case <-limit:
			_ = cmd.Process.Kill()
			t.Fatal("bundle did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err := <-done:
		t.Fatalf("open -W returned while fixture was alive: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("open -W failed to observe exit")
	}
	if time.Since(start) < time.Second {
		t.Fatal("open -W did not wait")
	}
}

func darwinLaunchFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bundle := filepath.Join(dir, "Water Launch Fixture.app")
	mac := filepath.Join(bundle, "Contents", "MacOS")
	if err := os.MkdirAll(mac, 0700); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleExecutable</key><string>fixture</string><key>CFBundleIdentifier</key><string>org.water.transactionfixture</string><key>CFBundlePackageType</key><string>APPL</string><key>LSUIElement</key><true/></dict></plist>`
	os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0600)
	src := filepath.Join(dir, "fixture.c")
	os.WriteFile(src, []byte(`#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
int main(int argc,char**argv){
 const char *ack=getenv("WATER_UPDATE_ACK"),*keep=getenv("WATER_UPDATE_PRESERVE_SERVER"),*error=getenv("WATER_UPDATE_ERROR");
 if(!ack||argc!=3||strcmp(argv[2],"argument with spaces"))return 12;
#ifdef OLD_VERSION
 argv[1]="legacy";
#endif
 if(strcmp(argv[1],"legacy") && (!keep||strcmp(keep,"1")))return 13;
 if(!strcmp(argv[1],"bare-exit"))return 0;
 char path[4096];snprintf(path,sizeof(path),"%s.pid",ack);
 FILE*f;
 char fixture[4096];snprintf(fixture,sizeof(fixture),"%s.fixture-pid",ack);f=fopen(fixture,"w");if(!f)return 17;fprintf(f,"%d",getpid());fclose(f);
 if(!strcmp(argv[1],"legacy-hang")){sleep(30);return 0;}
 if(strcmp(argv[1],"legacy")){f=fopen(path,"w");if(!f)return 14;fprintf(f,"%d",getpid());fclose(f);}
 if(!strcmp(argv[1],"fail"))return 7;
 if(!strcmp(argv[1],"hang")){sleep(30);return 0;}
 f=fopen(ack,"w");if(!f)return 15;
 if(!strcmp(argv[1],"legacy")){if(!error||!strlen(error))return 16;fprintf(f,"ready\n");}else fprintf(f,"%d",getpid());fclose(f);
 sleep(1);return 0;
}`), 0600)
	executable := filepath.Join(mac, "fixture")
	if out, err := exec.Command("/usr/bin/clang", src, "-o", executable).CombinedOutput(); err != nil {
		t.Fatalf("fixture compile: %v %s", err, out)
	}
	return executable
}

func TestDarwinInstallerLaunchAndLegacyRollback(t *testing.T) {
	executable := darwinLaunchFixture(t)
	for _, mode := range []string{"new", "legacy", "bare-exit", "hang", "legacy-hang"} {
		t.Run(mode, func(t *testing.T) {
			ack := filepath.Join(t.TempDir(), "ready")
			defer func() {
				if b, e := os.ReadFile(ack + ".fixture-pid"); e == nil {
					if pid, e := strconv.Atoi(string(b)); e == nil && pid > 1 {
						proc, _ := os.FindProcess(pid)
						_ = proc.Kill()
					}
				}
			}()
			message := ""
			if mode == "legacy" {
				message = "replacement failed"
			}
			p := Plan{Executable: executable, Arguments: []string{mode, "argument with spaces"}}
			err := launchAndWaitOptions(p, ack, message, mode == "legacy", 3*time.Second)
			switch mode {
			case "new", "legacy":
				if err != nil {
					t.Fatal(err)
				}
			case "bare-exit":
				if err == nil || errors.Is(err, errReplacementAlive) {
					t.Fatalf("clean open -W exit must confirm process exit: %v", err)
				}
			case "legacy-hang":
				if !errors.Is(err, errReplacementAlive) {
					t.Fatalf("unknown legacy process was unsafe to roll back: %v", err)
				}
			case "hang":
				if err == nil || errors.Is(err, errReplacementAlive) {
					t.Fatalf("owned hung replacement not stopped: %v", err)
				}
			}
		})
	}
	// The transaction installs a failing new bundle, restores its backup, then
	// runs the actual legacy restart wrapper, whose ACK has no PID marker.
	dir := t.TempDir()
	stage := filepath.Join(dir, "stage")
	os.Mkdir(stage, 0700)
	target := filepath.Join(dir, "Restored.app")
	source := filepath.Join(stage, "New.app")
	originalBundle := filepath.Dir(filepath.Dir(filepath.Dir(executable)))
	for _, dst := range []string{target, source} {
		if out, err := exec.Command("/bin/cp", "-R", originalBundle, dst).CombinedOutput(); err != nil {
			t.Fatalf("copy bundle: %v %s", err, out)
		}
	}
	// The restored executable implements only the legacy protocol and ignores
	// preservation env; it receives precisely the same arguments as the new GUI.
	if out, err := exec.Command("/usr/bin/clang", "-DOLD_VERSION", filepath.Join(filepath.Dir(originalBundle), "fixture.c"), "-o", filepath.Join(target, "Contents", "MacOS", "fixture")).CombinedOutput(); err != nil {
		t.Fatalf("compile legacy bundle: %v %s", err, out)
	}
	p := Plan{Directory: stage, Executable: filepath.Join(target, "Contents", "MacOS", "fixture"), Arguments: []string{"fail", "argument with spaces"}, Entries: []Entry{{source, target}}}
	err := replace(&p, func() error {
		return launchAndWaitOptions(p, filepath.Join(stage, "new-ready"), "", false, 3*time.Second)
	})
	if err == nil || !canRestartRestored(err) {
		t.Fatalf("replacement did not safely restore: %v", err)
	}
	if err := launchAndWaitWithError(p, filepath.Join(stage, "old-ready"), err.Error()); err != nil {
		t.Fatal("legacy rollback failed", err)
	}
}
