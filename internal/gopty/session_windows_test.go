//go:build windows

package gopty

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The test executable supplies deterministic native children without depending
// on installed shells or their profile settings.
func TestNativeChild(t *testing.T) {
	if os.Getenv("WATER_NATIVE_PTY_CHILD") == "" {
		return
	}
	switch os.Getenv("WATER_NATIVE_PTY_CHILD") {
	case "echo":
		fmt.Println("READY_NATIVE")
		var line string
		fmt.Scanln(&line)
		fmt.Println("REPLY_NATIVE:" + line)
	case "touch":
		os.WriteFile(os.Getenv("WATER_NATIVE_TOUCH"), []byte("ran"), 0600)
	case "markers":
		fmt.Print("\x1b]777;water-probe;start;123\aMARKER_TEXT\x1b]777;water-probe;end;0\a")
	case "properties":
		cwd, _ := os.Getwd()
		fmt.Println("ARG_NATIVE:" + os.Args[len(os.Args)-1])
		fmt.Println("ENV_NATIVE:" + os.Getenv("WATER_UNICODE"))
		fmt.Println("CWD_NATIVE:" + cwd)
	case "tail":
		fmt.Print("FINAL_NATIVE_OUTPUT")
	case "hold":
		fmt.Println("READY_NATIVE")
		time.Sleep(time.Minute)
	case "tree":
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChild$")
		cmd.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD=hold")
		if err := cmd.Start(); err != nil {
			panic(err)
		}
		fmt.Printf("CHILD_NATIVE:%d\n", cmd.Process.Pid)
		time.Sleep(time.Minute)
	}
	os.Exit(0)
}
func nativeSession(t *testing.T, mode string) *Session {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChild$")
	cmd.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD="+mode)
	s, err := Start(cmd, &Size{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func readUntil(t *testing.T, s *Session, needle string) string {
	t.Helper()
	s.SetReadDeadline(time.Now().Add(8 * time.Second))
	defer s.SetReadDeadline(time.Time{})
	var got strings.Builder
	buf := make([]byte, 4096)
	for !strings.Contains(got.String(), needle) {
		n, err := s.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			t.Fatalf("read %q: %v, got %q", needle, err, got.String())
		}
	}
	return got.String()
}
func TestNativeInputResizeAndDrain(t *testing.T) {
	s := nativeSession(t, "echo")
	readUntil(t, s, "READY_NATIVE")
	if err := s.Resize(&Size{Rows: 32, Cols: 101}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello\r")); err != nil {
		t.Fatal(err)
	}
	// Wait and output drain must run concurrently: ConPTY shutdown can flush.
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	var output bytes.Buffer
	_, err := io.Copy(&output, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "REPLY_NATIVE:hello") {
		t.Fatalf("missing final output: %q", output.String())
	}
}
func TestNativeIdleReadDeadlineAndResume(t *testing.T) {
	s := nativeSession(t, "echo")
	readUntil(t, s, "READY_NATIVE")
	s.SetReadDeadline(time.Now().Add(40 * time.Millisecond))
	buf := make([]byte, 4096)
	// A trailing newline can remain after READY; keep reading until deadline.
	for {
		_, err := s.Read(buf)
		if errors.Is(err, os.ErrDeadlineExceeded) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	s.SetReadDeadline(time.Time{})
	s.Write([]byte("aftercancel\r"))
	readUntil(t, s, "REPLY_NATIVE:aftercancel")
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	io.Copy(io.Discard, s)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestNativeKillOwnsDescendants(t *testing.T) {
	neighbor := exec.Command(os.Args[0], "-test.run=^TestNativeChild$")
	neighbor.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD=hold")
	if err := neighbor.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { neighbor.Process.Kill(); neighbor.Wait() }()
	s := nativeSession(t, "tree")
	out := readUntil(t, s, "CHILD_NATIVE:")
	// Consume the complete decimal PID even when ConPTY fragments the marker.
	for !strings.Contains(out[strings.Index(out, "CHILD_NATIVE:"):], "\n") {
		buf := make([]byte, 256)
		n, e := s.Read(buf)
		if e != nil {
			t.Fatal(e)
		}
		out += string(buf[:n])
	}
	var pid uint32
	fmt.Sscanf(out[strings.Index(out, "CHILD_NATIVE:"):], "CHILD_NATIVE:%d", &pid)
	child, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(child)
	if err := s.Kill(); err != nil {
		t.Fatal(err)
	}
	if result, err := windows.WaitForSingleObject(child, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatalf("descendant survived: result=%d err=%v", result, err)
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(neighbor.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(h)
	if result, _ := windows.WaitForSingleObject(h, 0); result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("unrelated process exited")
	}
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	io.Copy(io.Discard, s)
	<-done
}
func TestNativeCloseInterruptsIdleReader(t *testing.T) {
	s := nativeSession(t, "hold")
	readUntil(t, s, "READY_NATIVE")
	done := make(chan struct{})
	go func() { io.Copy(io.Discard, s); close(done) }()
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("close blocked")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("read remained blocked")
	}
}
func TestNativeCmd(t *testing.T) {
	s, err := Start(exec.Command("cmd.exe", "/c", "echo NATIVE_CMD"), &Size{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, err := io.ReadAll(s)
	t.Logf("output=%q error=%v", b, err)
	t.Logf("wait=%v", <-done)
	if !bytes.Contains(b, []byte("NATIVE_CMD")) {
		t.Fatal("missing output")
	}
}

func TestNativeUnicodeArgumentsEnvironmentAndDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "终端 space")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChild$", "--", `空 格 "quoted" \`)
	cmd.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD=properties", "WATER_UNICODE=before", "water_unicode=中文 value")
	cmd.Dir = dir
	s, err := Start(cmd, &Size{Rows: 40, Cols: 300})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	s.SetReadDeadline(time.Now().Add(8 * time.Second))
	output, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`ARG_NATIVE:空 格 "quoted" \`, "ENV_NATIVE:中文 value", "CWD_NATIVE:" + dir} {
		if !bytes.Contains(output, []byte(want)) {
			t.Fatalf("missing %q in %q", want, output)
		}
	}
}
func TestNativeWriteDeadline(t *testing.T) {
	s := nativeSession(t, "hold")
	readUntil(t, s, "READY_NATIVE")
	if err := s.SetWriteDeadline(time.Now().Add(60 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	// With neither a console input consumer nor an output drainer, the bounded
	// ConPTY pipes eventually backpressure this write.
	_, err := s.Write(bytes.Repeat([]byte("x"), 8<<20))
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("write did not time out: %v", err)
	}
	if err := s.SetWriteDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
}
func TestNativeStartFailureCleansUp(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	cmd.Dir = filepath.Join(t.TempDir(), "missing")
	if s, err := Start(cmd, &Size{Rows: 24, Cols: 80}); err == nil {
		s.Close()
		t.Fatal("invalid cwd started")
	}
}

func TestNativeMarkerPassthrough(t *testing.T) {
	s := nativeSession(t, "markers")
	done := make(chan error, 1)
	go func() { done <- s.Wait() }()
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	b, e := io.ReadAll(s)
	t.Logf("output=%q err=%v", b, e)
	if e != nil || !bytes.Contains(b, []byte("MARKER_TEXT")) || !bytes.Contains(b, []byte("water-probe;end;0")) {
		t.Fatal("ConPTY lost marker or rendered output")
	}
	<-done
}

func TestNativeJobAssignmentFailureNeverRunsChild(t *testing.T) {
	original := assignProcessToJob
	assignProcessToJob = func(windows.Handle, windows.Handle) error { return windows.ERROR_ACCESS_DENIED }
	defer func() { assignProcessToJob = original }()
	cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChild$")
	touch := filepath.Join(t.TempDir(), "must-not-run")
	cmd.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD=touch", "WATER_NATIVE_TOUCH="+touch)
	started := time.Now()
	s, err := Start(cmd, &Size{Rows: 24, Cols: 80})
	if err == nil {
		s.Close()
		t.Fatal("forced Job assignment failure succeeded")
	}
	if _, e := os.Stat(touch); !os.IsNotExist(e) {
		t.Fatal("suspended child ran before assignment failure cleanup")
	}
	if !strings.Contains(err.Error(), "assign terminal job") || time.Since(started) > 3*time.Second {
		t.Fatalf("failure cleanup: %v duration %s", err, time.Since(started))
	}
}
func TestNativeRepeatedLifecycleDoesNotLeakHandles(t *testing.T) {
	handleCount := func() uint32 {
		var n uint32
		r, _, e := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount").Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
		if r == 0 {
			t.Fatal(e)
		}
		return n
	}
	cycle := func() {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNativeChild$")
		cmd.Env = append(os.Environ(), "WATER_NATIVE_PTY_CHILD=tail")
		s, e := Start(cmd, &Size{Rows: 24, Cols: 80})
		if e != nil {
			t.Fatal(e)
		}
		done := make(chan error, 1)
		go func() { done <- s.Wait() }()
		io.Copy(io.Discard, s)
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		s.Close()
	}
	cycle()
	before := handleCount()
	for i := 0; i < 12; i++ {
		cycle()
	}
	after := handleCount()
	t.Logf("handles before=%d after=%d", before, after)
	if after > before+4 {
		t.Fatalf("terminal handles accumulated: before=%d after=%d", before, after)
	}
}
