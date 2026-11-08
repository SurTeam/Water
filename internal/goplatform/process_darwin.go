package goplatform

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"time"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

const detachedExecEnv = "WATER_INTERNAL_DETACHED_EXEC"

// Setsid alone does not remove LaunchServices' ownership of an application's
// descendants. A replacement launch can terminate those subordinate processes,
// including the updater, server and the supervisor holding the live PTYs.
// RunningBinaryReplaced reports whether executable was written after pid started.
// macOS TCC checks the signature of the file now at the process's path. A
// supervisor kept alive across an in-place upgrade therefore loses the app's
// Full Disk Access even though a newly started copy of the same path has it.
func RunningBinaryReplaced(pid int, executable string) (bool, error) {
	info, err := os.Stat(executable)
	if err != nil {
		return false, err
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return false, err
	}
	if len(procs) != 1 || int(procs[0].Proc.P_pid) != pid {
		return false, unix.ESRCH
	}
	started := time.Unix(procs[0].Proc.P_starttime.Sec, int64(procs[0].Proc.P_starttime.Usec)*1000)
	return info.ModTime().After(started), nil
}

func Detach(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if command.Env == nil {
		command.Env = os.Environ()
	}
	command.Env = append(command.Env, detachedExecEnv+"=1")
}

func init() {
	if os.Getenv(detachedExecEnv) != "1" {
		return
	}
	_ = os.Unsetenv(detachedExecEnv)
	if err := disclaimDetachedExec(); err != nil {
		fmt.Fprintln(os.Stderr, "water: detach application responsibility:", err)
		os.Exit(1)
	}
}

// Re-exec in place before initializing the server/updater. SETEXEC preserves the
// PID and inherited takeover descriptors, so exec.Cmd still owns and waits for
// exactly the process it launched. The marker is consumed before re-exec and
// cannot leak into shells. Resolve the macOS extension dynamically, as LLDB does.
func disclaimDetachedExec() error {
	lib, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return err
	}
	defer purego.Dlclose(lib)
	var initialize func(*uintptr) int32
	var destroy func(*uintptr) int32
	var flags func(*uintptr, int16) int32
	var disclaim func(*uintptr, uint32) int32
	var spawn func(*int32, string, uintptr, *uintptr, **byte, **byte) int32
	for _, symbol := range []struct {
		name string
		fn   any
	}{
		{"posix_spawnattr_init", &initialize},
		{"posix_spawnattr_destroy", &destroy},
		{"posix_spawnattr_setflags", &flags},
		{"responsibility_spawnattrs_setdisclaim", &disclaim},
		{"posix_spawn", &spawn},
	} {
		address, err := purego.Dlsym(lib, symbol.name)
		if err != nil {
			return err
		}
		purego.RegisterFunc(symbol.fn, address)
	}
	var attr uintptr
	if code := initialize(&attr); code != 0 {
		return syscall.Errno(code)
	}
	defer destroy(&attr)
	if code := flags(&attr, 0x0040 /* POSIX_SPAWN_SETEXEC */); code != 0 {
		return syscall.Errno(code)
	}
	if code := disclaim(&attr, 1); code != 0 {
		return syscall.Errno(code)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	argv, err := syscall.SlicePtrFromStrings(os.Args)
	if err != nil {
		return err
	}
	env, err := syscall.SlicePtrFromStrings(os.Environ())
	if err != nil {
		return err
	}
	var pid int32
	code := spawn(&pid, executable, 0, &attr, &argv[0], &env[0])
	runtime.KeepAlive(argv)
	runtime.KeepAlive(env)
	return fmt.Errorf("re-exec: %w", syscall.Errno(code))
}
