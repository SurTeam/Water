//go:build windows

package gopty

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Session owns the pseudoconsole and a job containing only this terminal's tree.
// Its process is assigned while suspended, before user code can spawn children.
type Session struct {
	*os.File    // overlapped output reader; registered with Go's Windows poller
	input       *os.File
	console     windows.Handle
	job         windows.Handle
	process     windows.Handle
	pid         int
	cwdPath     string
	code        uint32
	waitErr     error
	waitOnce    sync.Once
	closeOnce   sync.Once
	consoleOnce sync.Once
	consoleMu   sync.Mutex
	killMu      sync.Mutex
}

var pipeSerial atomic.Uint64
var assignProcessToJob = windows.AssignProcessToJobObject

// pipePair uses an overlapped parent end and synchronous ConPTY end. Ordinary
// anonymous pipes cannot support deadlines, which the resize barrier requires.
func pipePair(parentReads bool) (*os.File, windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`\\.\pipe\water-pty-%d-%d`, os.Getpid(), pipeSerial.Add(1)))
	if err != nil {
		return nil, 0, err
	}
	access := uint32(windows.PIPE_ACCESS_DUPLEX)
	clientAccess := uint32(windows.GENERIC_READ)
	if parentReads {
		access = windows.PIPE_ACCESS_DUPLEX
		clientAccess = windows.GENERIC_WRITE
	}
	parent, err := windows.CreateNamedPipe(name, access|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 65536, 65536, 0, nil)
	if err != nil {
		return nil, 0, err
	}
	client, err := windows.CreateFile(name, clientAccess, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		windows.CloseHandle(parent)
		return nil, 0, err
	}
	// Opening the client establishes the connection before ConnectNamedPipe.
	err = windows.ConnectNamedPipe(parent, nil)
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		windows.CloseHandle(client)
		windows.CloseHandle(parent)
		return nil, 0, err
	}
	f := os.NewFile(uintptr(parent), "water-pty-pipe")
	return f, client, nil
}

func Start(cmd *exec.Cmd, size *Size) (session *Session, err error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	preparePowerShell(cmd)
	s := &Session{}
	var consoleInput, consoleOutput windows.Handle
	defer func() {
		if err != nil {
			s.Close()
		}
		if consoleInput != 0 {
			windows.CloseHandle(consoleInput)
		}
		if consoleOutput != 0 {
			windows.CloseHandle(consoleOutput)
		}
	}()
	s.input, consoleInput, err = pipePair(false)
	if err != nil {
		return nil, err
	}
	s.File, consoleOutput, err = pipePair(true)
	if err != nil {
		return nil, err
	}
	if err = s.File.SetReadDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("ConPTY output poller: %w", err)
	}
	if err = s.input.SetWriteDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("ConPTY input poller: %w", err)
	}
	if err = windows.CreatePseudoConsole(windows.Coord{X: int16(size.Cols), Y: int16(size.Rows)}, consoleInput, consoleOutput, 0, &s.console); err != nil {
		return nil, err
	}
	s.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(s.job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	attrs, e := windows.NewProcThreadAttributeList(1)
	if e != nil {
		return nil, e
	}
	defer attrs.Delete()
	// This attribute takes the opaque HPCON value itself, unlike attributes
	// that take the address of a value. Reinterpret its bits without converting
	// a numeric Windows handle into a Go heap pointer.
	consoleValue := *(*unsafe.Pointer)(unsafe.Pointer(&s.console))
	if err = attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, consoleValue, unsafe.Sizeof(s.console)); err != nil {
		return nil, err
	}
	path := cmd.Path
	if path == "" {
		if len(cmd.Args) == 0 {
			return nil, errors.New("terminal program is required")
		}
		path = cmd.Args[0]
	}
	if !strings.ContainsAny(path, `\/:`) {
		path, err = exec.LookPath(path)
		if err != nil {
			return nil, err
		}
	}
	app, e := windows.UTF16PtrFromString(path)
	if e != nil {
		return nil, e
	}
	args := cmd.Args
	if len(args) == 0 {
		args = []string{path}
	}
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = syscall.EscapeArg(arg)
	}
	command, e := windows.UTF16PtrFromString(strings.Join(quoted, " "))
	if e != nil {
		return nil, e
	}
	var dir *uint16
	if cmd.Dir != "" {
		dir, e = windows.UTF16PtrFromString(cmd.Dir)
		if e != nil {
			return nil, e
		}
	}
	env := append([]string{}, cmd.Env...)
	if cmd.Env == nil {
		env = os.Environ()
	}
	values := make(map[string]string)
	for _, entry := range env {
		if strings.IndexByte(entry, 0) >= 0 {
			return nil, errors.New("terminal environment contains NUL")
		}
		if len(entry) < 2 {
			continue
		}
		if i := strings.IndexByte(entry[1:], '='); i >= 0 {
			key := strings.ToUpper(entry[:i+1])
			values[key] = entry
		}
	}
	env = env[:0]
	for _, entry := range values {
		env = append(env, entry)
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	// Double NUL terminates the Unicode block, including the empty case.
	block := strings.Join(env, "\x00") + "\x00\x00"
	env16 := utf16.Encode([]rune(block))
	info := windows.StartupInfoEx{}
	info.Cb = uint32(unsafe.Sizeof(info))
	// Prevent CreateProcess from copying the parent SSH/GUI standard handles.
	// ConPTY supplies the console handles when these explicit fields are zero.
	info.Flags = windows.STARTF_USESTDHANDLES
	info.ProcThreadAttributeList = attrs.List()
	var pi windows.ProcessInformation
	err = windows.CreateProcess(app, command, nil, nil, false, windows.CREATE_SUSPENDED|windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT, &env16[0], dir, &info.StartupInfo, &pi)
	runtime.KeepAlive(attrs)
	runtime.KeepAlive(env16)
	if err != nil {
		return nil, err
	}
	s.process = pi.Process
	s.pid = int(pi.ProcessId)
	var creation, exit, kernel, user windows.Filetime
	if windows.GetProcessTimes(s.process, &creation, &exit, &kernel, &user) == nil {
		s.cwdPath = CwdFile(s.pid, strconv.FormatUint(uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime), 10))
	}
	defer windows.CloseHandle(pi.Thread)
	if err = assignProcessToJob(s.job, s.process); err != nil {
		windows.TerminateProcess(s.process, 1)
		windows.WaitForSingleObject(s.process, windows.INFINITE)
		return nil, fmt.Errorf("assign terminal job: %w", err)
	}
	if _, err = windows.ResumeThread(pi.Thread); err != nil {
		s.Kill()
		windows.WaitForSingleObject(s.process, windows.INFINITE)
		return nil, err
	}
	return s, nil
}
func (s *Session) PID() int                           { return s.pid }
func (s *Session) ExitCode() int                      { return int(s.code) }
func (s *Session) Write(p []byte) (int, error)        { return s.input.Write(p) }
func (s *Session) SetWriteDeadline(t time.Time) error { return s.input.SetWriteDeadline(t) }
func (s *Session) Resize(size *Size) error {
	s.consoleMu.Lock()
	defer s.consoleMu.Unlock()
	if s.console == 0 {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(s.console, windows.Coord{X: int16(size.Cols), Y: int16(size.Rows)})
}
func (s *Session) ForegroundGroup() (int, error) {
	return s.foregroundProcess()
}
func (s *Session) Wait() error {
	s.waitOnce.Do(func() {
		_, s.waitErr = windows.WaitForSingleObject(s.process, windows.INFINITE)
		if s.waitErr == nil {
			s.waitErr = windows.GetExitCodeProcess(s.process, &s.code)
		}
		if s.waitErr == nil && s.code != 0 {
			s.waitErr = fmt.Errorf("process exited with code %d", s.code)
		}
		s.closeConsole()
	})
	return s.waitErr
}
func (s *Session) Kill() error {
	s.killMu.Lock()
	defer s.killMu.Unlock()
	if s.job == 0 {
		return nil
	}
	return windows.TerminateJobObject(s.job, 1)
}
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.Kill()
		// ClosePseudoConsole may synchronously flush output. Keep a reader alive
		// while closing it, then close the pollable pipe to wake an idle reader.
		if s.input != nil {
			s.input.Close()
		}
		if s.File != nil {
			s.File.Close()
		}
		if s.process != 0 {
			s.Wait()
		} else {
			s.closeConsole()
		}
		s.killMu.Lock()
		if s.job != 0 {
			windows.CloseHandle(s.job)
			s.job = 0
		}
		s.killMu.Unlock()
		if s.process != 0 {
			if s.cwdPath != "" {
				os.Remove(s.cwdPath)
			}
			windows.CloseHandle(s.process)
		}
	})
	return nil
}

func (s *Session) closeConsole() {
	s.consoleOnce.Do(func() {
		s.consoleMu.Lock()
		defer s.consoleMu.Unlock()
		if s.console != 0 {
			windows.ClosePseudoConsole(s.console)
			s.console = 0
		}
	})
}
