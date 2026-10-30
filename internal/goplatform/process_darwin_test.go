package goplatform

import (
	"encoding/json"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/ebitengine/purego"
)

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
	for _, detached := range []bool{false, true} {
		t.Run(map[bool]string{false: "setsid-only", true: "independent-application"}[detached], func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			defer w.Close()
			cmd := exec.Command(os.Args[0], "-test.run=^TestDetachedApplicationResponsibility$")
			cmd.Env = append(os.Environ(), "WATER_TEST_DETACHED_CHILD=1")
			cmd.ExtraFiles = []*os.File{w}
			if detached {
				Detach(cmd)
			} else {
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
			if result.PID != cmd.Process.Pid || (result.Responsible == result.PID) != detached || result.Marker != "" {
				t.Fatalf("detached child retained application ownership or lost launch identity: %+v", result)
			}
		})
	}
}
