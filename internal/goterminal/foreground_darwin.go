package goterminal

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/SurTeam/Water/internal/goagent"
	"golang.org/x/sys/unix"
)

// Query the selected PTY leaders directly. Spawning ps for every refresh adds
// process-launch latency and splits quoted arguments; sysctl preserves argv and
// gives microsecond start identity without another process or a full process scan.
func sampleProcessGroups(ctx context.Context, groups []int) (map[int]goagent.Process, error) {
	processes := make(map[int]goagent.Process, len(groups))
	for _, group := range groups {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("foreground sampling failed: %w", err)
		}
		before, err := darwinProcessInfo(group)
		if vanishedProcess(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("foreground identity sampling failed: %w", err)
		}
		if int(before.Proc.P_pid) != group || int(before.Eproc.Pgid) != group {
			continue
		}
		raw, err := unix.SysctlRaw("kern.procargs2", group)
		if vanishedProcess(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("foreground argument sampling failed: %w", err)
		}
		args, err := parseDarwinProcessArgs(raw)
		if err != nil {
			return nil, err
		}
		// The leader may exec or exit between the two queries. Never join argv from
		// a reused PID to the previous run's identity.
		after, err := darwinProcessInfo(group)
		if vanishedProcess(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("foreground identity recheck failed: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("foreground sampling failed: %w", err)
		}
		if after.Proc.P_pid != before.Proc.P_pid || after.Eproc.Pgid != before.Eproc.Pgid || after.Proc.P_starttime != before.Proc.P_starttime {
			continue
		}
		processes[group] = goagent.Process{
			PID: group, PGID: group,
			Started: fmt.Sprintf("%d.%06d", before.Proc.P_starttime.Sec, before.Proc.P_starttime.Usec),
			Name:    strings.TrimLeft(filepath.Base(args[0]), "-"), Args: args,
		}
	}
	return processes, nil
}

func darwinProcessInfo(pid int) (*unix.KinfoProc, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.pid", pid)
	if err != nil {
		return nil, err
	}
	if len(processes) == 0 {
		return nil, unix.ESRCH
	}
	if len(processes) != 1 {
		return nil, errors.New("ambiguous foreground identity")
	}
	return &processes[0], nil
}

func vanishedProcess(err error) bool {
	return errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT)
}

// KERN_PROCARGS2 starts with argc and an executable path, followed by padding,
// argc NUL-terminated argv entries, then the environment. Never parse the latter.
func parseDarwinProcessArgs(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, errors.New("truncated foreground argument count")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	raw = raw[4:]
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		return nil, errors.New("truncated foreground executable path")
	}
	executable := string(raw[:end])
	raw = bytes.TrimLeft(raw[end+1:], "\x00")
	if argc < 1 || argc > len(raw) {
		return nil, errors.New("invalid foreground argument count")
	}
	args := make([]string, 0, argc)
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return nil, errors.New("truncated foreground arguments")
		}
		args = append(args, string(raw[:end]))
		raw = raw[end+1:]
	}
	if args[0] == "" {
		args[0] = executable
	}
	return args, nil
}
