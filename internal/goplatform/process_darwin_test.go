package goplatform

import (
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/ebitengine/purego"
)

func TestRunningBinaryReplaced(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := RunningBinaryReplaced(os.Getpid(), self)
	if err != nil || replaced {
		t.Fatalf("current process binary = replaced %v (%v)", replaced, err)
	}
	file, err := os.CreateTemp(t.TempDir(), "replaced")
	if err != nil {
		t.Fatal(err)
	}
	file.Close()
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(file.Name(), future, future); err != nil {
		t.Fatal(err)
	}
	replaced, err = RunningBinaryReplaced(os.Getpid(), file.Name())
	if err != nil || !replaced {
		t.Fatalf("newer file = replaced %v (%v)", replaced, err)
	}
}

func TestDetachedApplicationResponsibility(t *testing.T) {
	if os.Getenv("WATER_TEST_DETACHED_CHILD") == "1" {
		lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if err != nil {
			t.Fatal(err)
		}
		var responsible func(int32) int32
		purego.RegisterLibFunc(&responsible, lib, "responsibility_get_pid_responsible_for_pid")
		file := os.NewFile(3, "inherited takeover pipe")
		defer file.Close()
		if err := json.NewEncoder(file).Encode(map[string]any{
			"pid": os.Getpid(), "responsible": responsible(int32(os.Getpid())),
			"marker": os.Getenv(detachedExecEnv),
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	var baselineIndependent bool
	for _, mode := range []string{"baseline", "setsid-only", "independent-application"} {
		t.Run(mode, func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedApplicationResponsibility$")
			cmd.Env = append(os.Environ(), "WATER_TEST_DETACHED_CHILD=1")
			cmd.ExtraFiles = []*os.File{w}
			if mode == "independent-application" {
				Detach(cmd)
			} else if mode == "setsid-only" {
				cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("detached child: %v: %s", err, out)
			}
			w.Close()
			var result struct {
				PID, Responsible int
				Marker           string
			}
			if err := json.NewDecoder(r).Decode(&result); err != nil {
				t.Fatal(err)
			}
			independent := result.Responsible == result.PID
			if mode == "baseline" {
				baselineIndependent = independent
			}
			wantIndependent := baselineIndependent || mode == "independent-application"
			if result.PID != cmd.Process.Pid || independent != wantIndependent || result.Marker != "" {
				t.Fatalf("detached child retained application ownership or lost launch identity: %+v", result)
			}
		})
	}
}
