//go:build windows

package gopty

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessInfo queries retained native identity and command line. It does not
// confer permission to terminate the numeric PID.
var isProcessInJob = windows.NewLazySystemDLL("kernel32.dll").NewProc("IsProcessInJob")

type ProcessInfo struct {
	PID           int
	Started, Name string
	Args          []string
}

func InspectProcess(ctx context.Context, pid int) (ProcessInfo, error) {
	p := ProcessInfo{PID: pid}
	if err := ctx.Err(); err != nil {
		return p, err
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return p, err
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user); err != nil {
		return p, err
	}
	p.Started = strconv.FormatUint(uint64(created.HighDateTime)<<32|uint64(created.LowDateTime), 10)
	path := make([]uint16, 32768)
	n := uint32(len(path))
	if err = windows.QueryFullProcessImageName(h, 0, &path[0], &n); err != nil {
		return p, err
	}
	p.Name = strings.TrimSuffix(strings.ToLower(filepath.Base(windows.UTF16ToString(path[:n]))), ".exe")
	buffer := make([]byte, 128<<10)
	var returned uint32
	if err = windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buffer[0]), uint32(len(buffer)), &returned); err == nil {
		u := (*windows.NTUnicodeString)(unsafe.Pointer(&buffer[0]))
		p.Args, _ = windows.DecomposeCommandLine(u.String())
	}
	if len(p.Args) == 0 {
		p.Args = []string{windows.UTF16ToString(path[:n])}
	}
	return p, ctx.Err()
}

// foregroundProcess is a conservative representative of owned Job members.
// Windows has no POSIX foreground process group: background jobs cannot be
// distinguished reliably, and any active child makes pane.execute reject idle.
func (s *Session) foregroundProcess() (int, error) {
	s.killMu.Lock()
	defer s.killMu.Unlock()
	if s.job == 0 {
		return 0, os.ErrClosed
	}
	// Job membership limits discovery to this terminal, including detached child
	// processes. A bounded list fails explicitly rather than sampling other jobs.
	buffer := make([]uintptr, 4098)
	var returned uint32
	if err := windows.QueryInformationJobObject(s.job, windows.JobObjectBasicProcessIdList, uintptr(unsafe.Pointer(&buffer[0])), uint32(len(buffer))*uint32(unsafe.Sizeof(uintptr(0))), &returned); err != nil {
		return 0, err
	}
	counts := (*[2]uint32)(unsafe.Pointer(&buffer[0]))
	count := int(counts[1])
	offset := 8 / int(unsafe.Sizeof(uintptr(0)))
	if count > len(buffer)-offset {
		return 0, fmt.Errorf("terminal process list exceeds limit")
	}
	selected := s.pid
	newest := ""
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	for _, value := range buffer[offset : count+offset] {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		pid := int(value)
		if pid == s.pid {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
		if e != nil {
			continue
		}
		var member int32
		ok, _, _ := isProcessInJob.Call(uintptr(h), uintptr(s.job), uintptr(unsafe.Pointer(&member)))
		if ok == 0 || member == 0 {
			windows.CloseHandle(h)
			continue
		}
		p, err := InspectProcess(ctx, pid)
		windows.CloseHandle(h)
		if err != nil {
			continue
		}
		switch p.Name {
		case "conhost", "openconsole":
			continue
		}
		// Prefer recognized agent launchers over their helper descendants.
		agent := false
		for _, arg := range p.Args {
			a := strings.ToLower(arg)
			if strings.Contains(a, "codex") || strings.Contains(a, "claude") || strings.Contains(a, "opencode") || strings.Contains(a, "pi-coding-agent") {
				agent = true
				break
			}
		}
		if agent {
			return pid, nil
		}
		if p.Started > newest {
			newest = p.Started
			selected = pid
		}
	}
	return selected, nil
}
func CwdFile(pid int, started string) string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("water-powershell-cwd-%d-%s.txt", pid, started))
}
func ProcessDirectory(ctx context.Context, pid int) string {
	p, err := InspectProcess(ctx, pid)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(CwdFile(pid, p.Started))
	if err != nil || len(b) > 32768 {
		return ""
	}
	dir := strings.TrimPrefix(string(b), "\ufeff")
	if st, err := os.Stat(dir); err == nil && st.IsDir() && filepath.IsAbs(dir) {
		return dir
	}
	return ""
}
