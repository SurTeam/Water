//go:build windows

package goterminal

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

// PowerShell owns this nested job inside the terminal's existing job. The
// command stays suspended until assignment succeeds, so no user descendant can
// escape. Finally closes the job even when PowerShell cancels the pipeline.
const windowsExecuteNative = `
using System;
using System.ComponentModel;
using System.Runtime.InteropServices;
using System.Text;
public sealed class WaterExecuteJob : IDisposable {
 [StructLayout(LayoutKind.Sequential,CharSet=CharSet.Unicode)] struct SI {public int cb;public string reserved,desktop,title;public int x,y,xs,ys,xc,yc,fill,flags;public short show,reserved2;public IntPtr extra,input,output,error;}
 [StructLayout(LayoutKind.Sequential)] struct PI {public IntPtr process,thread;public int pid,tid;}
 [StructLayout(LayoutKind.Sequential)] struct BASIC {public long processTime,jobTime;public uint flags;public UIntPtr min,max;public uint active;public UIntPtr affinity;public uint priority,scheduling;}
 [StructLayout(LayoutKind.Sequential)] struct LIMIT {public BASIC basic;public ulong r1,r2,r3,r4,r5,r6;public UIntPtr processMemory,jobMemory,peakProcess,peakJob;}
 [DllImport("kernel32",CharSet=CharSet.Unicode,SetLastError=true)] static extern IntPtr CreateJobObject(IntPtr a,string n);
 [DllImport("kernel32",SetLastError=true)] static extern bool SetInformationJobObject(IntPtr j,int c,ref LIMIT v,int n);
 [DllImport("kernel32",CharSet=CharSet.Unicode,SetLastError=true)] static extern bool CreateProcess(string app,StringBuilder command,IntPtr a,IntPtr b,bool inherit,uint flags,IntPtr env,string cwd,ref SI si,out PI pi);
 [DllImport("kernel32",SetLastError=true)] static extern bool AssignProcessToJobObject(IntPtr j,IntPtr p);
 [DllImport("kernel32",SetLastError=true)] static extern uint ResumeThread(IntPtr t);
 [DllImport("kernel32",SetLastError=true)] static extern bool GetExitCodeProcess(IntPtr p,out uint code);
 [DllImport("kernel32",SetLastError=true)] static extern bool TerminateJobObject(IntPtr j,uint code);
 [DllImport("kernel32",SetLastError=true)] static extern bool TerminateProcess(IntPtr p,uint code);
 [DllImport("kernel32")] static extern uint WaitForSingleObject(IntPtr p,uint timeout);
 [DllImport("kernel32")] static extern bool CloseHandle(IntPtr p);
 IntPtr job,process;
 public int PID {get;private set;}
 public WaterExecuteJob(string app,string command,string cwd) {
  job=CreateJobObject(IntPtr.Zero,null);if(job==IntPtr.Zero)throw new Win32Exception();
  try { LIMIT limits=new LIMIT();limits.basic.flags=0x2000;
   if(!SetInformationJobObject(job,9,ref limits,Marshal.SizeOf(limits)))throw new Win32Exception();
   SI si=new SI();si.cb=Marshal.SizeOf(si);PI pi;
   if(!CreateProcess(app,new StringBuilder(command),IntPtr.Zero,IntPtr.Zero,false,4,IntPtr.Zero,cwd,ref si,out pi))throw new Win32Exception();
   process=pi.process;PID=pi.pid;
   try {if(!AssignProcessToJobObject(job,process)){TerminateProcess(process,1);throw new Win32Exception();}
    if(ResumeThread(pi.thread)==0xffffffff){TerminateProcess(process,1);throw new Win32Exception();}
   } finally {CloseHandle(pi.thread);}
  } catch {Dispose();throw;}
 }
 public bool Exited {get{return WaitForSingleObject(process,0)==0;}}
 public int ExitCode {get{uint code;if(!GetExitCodeProcess(process,out code))throw new Win32Exception();return unchecked((int)code);}}
 public void Dispose(){int error=0;if(job!=IntPtr.Zero){if(!TerminateJobObject(job,130))error=Marshal.GetLastWin32Error();CloseHandle(job);job=IntPtr.Zero;}if(process!=IntPtr.Zero){if(WaitForSingleObject(process,2000)!=0)error=258;CloseHandle(process);process=IntPtr.Zero;}if(error!=0)throw new Win32Exception(error);}
}
`

func (t *Terminal) executePlatform(ctx context.Context, opts ExecuteOptions) (ExecuteResult, bool, error) {
	var result ExecuteResult
	if !interactivePowerShell(t.program, t.args) {
		return result, true, errors.New("Windows execution requires an interactive PowerShell shell")
	}
	shell := t.processID()
	if fg, err := t.ForegroundGroup(); err != nil || fg != shell {
		return result, true, errors.New("terminal shell is not idle in the foreground")
	}
	dir, err := os.MkdirTemp("", "water-execute-")
	if err != nil {
		return result, true, err
	}
	submitted := false
	statusPath := filepath.Join(dir, "status")
	defer func() {
		if !submitted {
			os.RemoveAll(dir)
		} else if _, e := os.Stat(statusPath); e == nil {
			os.RemoveAll(dir)
		}
	}()
	token := strings.ReplaceAll(uuid.NewString(), "-", "")
	startMark, endMark := "WATER_START_"+token, "WATER_END_"+token
	status := filepath.Join(dir, "status")
	cancelFile := filepath.Join(dir, "cancel")
	failureFile := filepath.Join(dir, "failure")
	commandFile := filepath.Join(dir, "command.txt")
	if err = os.WriteFile(commandFile, []byte(opts.Command), 0600); err != nil {
		return result, true, err
	}
	child := `$global:LASTEXITCODE=$null;$global:WaterExecuteSuccess=$true;try { & ([scriptblock]::Create([IO.File]::ReadAllText('` + psQuote(commandFile) + `')+[Environment]::NewLine+'; $global:WaterExecuteSuccess=$?')) | Out-Default;if($global:WaterExecuteSuccess){exit 0};if(($null -ne $LASTEXITCODE) -and ($LASTEXITCODE -ne 0)){exit ([int]$LASTEXITCODE)};exit 1 }catch{[Console]::Error.WriteLine($_.ToString());exit 1}`
	// The child uses the exact PowerShell executable of the pane; exported env
	// inherits naturally and cwd is taken from the shell's filesystem provider.
	script := `$waterJob=$null;$waterCode=130
[Console]::WriteLine('` + startMark + `')
try {
 if(-not ('WaterExecuteJob' -as [type])){Add-Type -TypeDefinition @'
` + windowsExecuteNative + `
'@ -ErrorAction Stop}
 if([IO.File]::Exists('` + psQuote(cancelFile) + `')){throw 'Execution cancelled before child startup'}
 $waterExe=(Get-Process -Id $PID).Path
 $waterChild='` + psEncoded(child) + `'
 $waterJob=[WaterExecuteJob]::new($waterExe,('"'+$waterExe+'" -NoLogo -NoProfile -NonInteractive -EncodedCommand '+$waterChild),$PWD.ProviderPath)
 while((-not $waterJob.Exited) -and (-not [IO.File]::Exists('` + psQuote(cancelFile) + `'))){Start-Sleep -Milliseconds 15}
 if($waterJob.Exited){$waterCode=$waterJob.ExitCode}
}catch{
 if(-not [IO.File]::Exists('` + psQuote(cancelFile) + `')){[IO.File]::WriteAllText('` + psQuote(failureFile) + `',$_.ToString())}
}finally{
 if($null -ne $waterJob){try{$waterJob.Dispose()}catch{[IO.File]::WriteAllText('` + psQuote(failureFile) + `',$_.ToString())}}
 [IO.File]::WriteAllText('` + psQuote(status) + `',[string]$waterCode)
 [Console]::WriteLine('` + endMark + `')
}`
	path := filepath.Join(dir, "run.ps1")
	if err = os.WriteFile(path, append([]byte{0xef, 0xbb, 0xbf}, []byte(script)...), 0600); err != nil {
		return result, true, err
	}
	events, done, cancel := t.Subscribe()
	defer cancel()
	size := t.Size()
	em := govt.New(size.Columns, size.Lines, 10000)
	defer em.Close()
	var seq uint64
	apply := func(ev goprotocol.TerminalEvent) {
		switch ev.Kind {
		case goprotocol.OutputEvent:
			em.WriteReplay(ev.Data)
		case goprotocol.ResizeEvent:
			em.Resize(ev.Size.Columns, ev.Size.Lines)
		}
	}
	for _, ev := range t.Replay() {
		apply(ev)
		seq = max(seq, ev.Seq)
	}
	deadline := time.NewTimer(opts.Timeout)
	defer deadline.Stop()
	if err = t.executeWrite([]byte("& ([scriptblock]::Create([IO.File]::ReadAllText('" + psQuote(path) + "')))\r")); err != nil {
		return result, true, err
	}
	submitted = true
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	var stopAt time.Time
	var executeErr error
	var scanTail []byte
	ctxDone := ctx.Done()
	invalid := false
	stop := func() {
		if stopAt.IsZero() {
			stopAt = time.Now()
			if e := os.WriteFile(cancelFile, []byte("cancel"), 0600); e != nil {
				executeErr = e
			} else {
				result.Signals = append(result.Signals, "JOB_KILL")
			}
		}
	}
	// ConPTY redraws and coalesces screen changes; OSC writes can overtake text.
	// Visible random markers are consumed only after they reach the emulator.
	capture := func() bool {
		cursor := em.ReadContentCursor()
		rows := min(cursor.TotalRows, govt.MaxContentRows)
		row := cursor.TotalRows - rows
		content, e := em.ReadContent(&row, &rows)
		if e != nil {
			return false
		}
		lines := content.Lines
		first, last := -1, -1
		// Match across soft-wrapped marker rows, including very narrow panes.
		flat := strings.Join(lines, "")
		startIndex, endIndex := strings.LastIndex(flat, startMark), strings.LastIndex(flat, endMark)
		offset := 0
		for i, line := range lines {
			next := offset + len(line)
			if startIndex >= 0 && startIndex+len(startMark) > offset && startIndex+len(startMark) <= next {
				first = i
			}
			if endIndex >= offset && endIndex < next {
				last = i
			}
			offset = next
		}
		if last < 0 || first >= last {
			return false
		}
		if first < 0 {
			first = max(-1, last-opts.MaxRows)
			result.Truncated = true
		}
		lines = append([]string(nil), lines[first+1:last]...)
		// Terminal line padding and blank rows before the marker are display state.
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > opts.MaxRows {
			lines = lines[len(lines)-opts.MaxRows:]
			result.Truncated = true
		}
		result.Lines = lines
		result.Text = strings.Join(lines, "\n")
		result.Truncated = result.Truncated || invalid
		result.AltScreen = result.AltScreen || cursor.AltScreen
		if len(result.Text) > opts.MaxBytes {
			b := []byte(result.Text[:opts.MaxBytes])
			for !utf8.Valid(b) {
				b = b[:len(b)-1]
			}
			result.Text = string(b)
			result.Lines = strings.Split(result.Text, "\n")
			result.Truncated = true
		}
		return true
	}
	finish := func() (ExecuteResult, bool, error) {
		b, e := os.ReadFile(status)
		if e != nil {
			return result, true, fmt.Errorf("PowerShell execution cleanup was not confirmed: %w", e)
		}
		n, e := strconv.Atoi(string(b))
		if e != nil {
			return result, true, e
		}
		result.ExitCode = &n
		if failure, e := os.ReadFile(failureFile); e == nil {
			return result, true, fmt.Errorf("PowerShell command job: %s", failure)
		}
		return result, true, executeErr
	}
	for {
		select {
		case ev := <-events:
			if ev.Seq <= seq {
				continue
			}
			if ev.Seq != seq+1 {
				executeErr = errors.New("terminal execution stream has a sequence gap")
				stop()
			}
			seq = ev.Seq
			if ev.Kind == goprotocol.OutputEvent {
				scan := append(scanTail, ev.Data...)
				if executeAltEscape.Match(scan) {
					result.AltScreen = true
					invalid = true
				}
				scanTail = append([]byte(nil), scan[max(0, len(scan)-64):]...)
			}

			if ev.Kind == goprotocol.ResizeEvent {
				invalid = true
				size = ev.Size
			}
			if ev.Kind == goprotocol.ExitEvent {
				return result, true, errors.New("terminal shell exited during execution")
			}
			apply(ev)
			if capture() {
				return finish()
			}
		case <-tick.C:
			if !stopAt.IsZero() && time.Since(stopAt) > 3*time.Second {
				result.Truncated = true
				return result, true, fmt.Errorf("PowerShell did not confirm command job cleanup; cancellation artifacts retained at %s", dir)
			}
			if capture() {
				return finish()
			}
		case <-deadline.C:
			result.TimedOut = true
			stop()
		case <-ctxDone:
			executeErr = ctx.Err()
			ctxDone = nil
			stop()
		case <-done:
			return result, true, errors.New("terminal subscription closed during execution")
		case <-t.closed:
			return result, true, errors.New("terminal closed during execution")
		}
	}
}

func psQuote(s string) string { return strings.ReplaceAll(s, "'", "''") }
func psEncoded(s string) string {
	b := make([]byte, 0, len(s)*2)
	for _, r := range s {
		if r > 0xffff {
			r -= 0x10000
			for _, u := range []rune{0xd800 + (r >> 10), 0xdc00 + (r & 1023)} {
				b = append(b, byte(u), byte(u>>8))
			}
		} else {
			b = append(b, byte(r), byte(r>>8))
		}
	}
	return base64.StdEncoding.EncodeToString(b)
}
func interactivePowerShell(program string, args []string) bool {
	switch strings.ToLower(filepath.Base(program)) {
	case "powershell.exe", "pwsh.exe", "powershell", "pwsh":
	default:
		return false
	}
	for _, arg := range args {
		switch strings.ToLower(arg) {
		case "-nologo", "-noprofile", "-noexit":
		default:
			return false
		}
	}
	return true
}
