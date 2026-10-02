package goterminal

import (
	"bytes"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestExitIsSequencedAfterFinalOutput(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{"-c", "printf final-output"}, goprotocol.TerminalSize{Columns:80, Lines:24})
	if err != nil { t.Fatal(err) }
	defer r.CloseAll()

	events, done, cancel := term.Subscribe()
	defer cancel()
	deadline := time.NewTimer(2*time.Second)
	defer deadline.Stop()
	for {
		select {
		case ev := <-events:
			if ev.Kind == goprotocol.ExitEvent {
				replay := term.Replay()
				if len(replay)==0 || replay[len(replay)-1].Kind != goprotocol.ExitEvent {
					t.Fatalf("exit is not last event: %#v", replay)
				}
				var out []byte
				for _, item := range replay {
					if item.Kind == goprotocol.OutputEvent { out=append(out,item.Data...) }
				}
				if !bytes.Contains(out,[]byte("final-output")) {
					t.Fatalf("missing final output: %q",out)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-deadline.C:
			t.Fatal("timed out waiting for exit")
		}
	}
}


func TestCloseTerminatesProcessGroup(t *testing.T) {
	r := NewRegistry()
	term, err := r.Spawn("/bin/sh", []string{
		"-c",
		"sleep 30 & child=$!; printf 'CHILD:%s\\n' \"$child\"; wait",
	}, goprotocol.TerminalSize{Columns:80, Lines:24})
	if err != nil {
		t.Fatal(err)
	}
	defer r.CloseAll()

	deadline := time.Now().Add(2 * time.Second)
	var childPID int
	for time.Now().Before(deadline) {
		var raw []byte
		for _, ev := range term.Replay() {
			if ev.Kind == goprotocol.OutputEvent {
				raw = append(raw, ev.Data...)
			}
		}
		text := string(raw)
		if idx := strings.Index(text, "CHILD:"); idx >= 0 {
			line := text[idx+len("CHILD:"):]
			if end := strings.IndexAny(line, "\r\n"); end >= 0 {
				line = line[:end]
			}
			childPID, _ = strconv.Atoi(strings.TrimSpace(line))
			if childPID > 1 {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if childPID <= 1 {
		t.Fatal("child PID was not reported")
	}

	if err := term.Close(); err != nil {
		t.Fatal(err)
	}
	killDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(killDeadline) {
		if err := syscall.Kill(childPID, 0); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	_ = syscall.Kill(childPID, syscall.SIGKILL)
	t.Fatalf("terminal descendant %d survived process-group close", childPID)
}


func TestPTYOutputIsCoalescedBeforeFanout(t *testing.T) {
	r:=NewRegistry()
	term,err:=r.Spawn(
		"/bin/sh",
		[]string{"-c","yes WATER_BATCH | head -c 1048576"},
		goprotocol.TerminalSize{Columns:80,Lines:24},
	)
	if err!=nil{t.Fatal(err)}
	defer r.CloseAll()

	events,done,cancel:=term.Subscribe()
	defer cancel()
	timer:=time.NewTimer(5*time.Second)
	defer timer.Stop()
	outputEvents:=0
	outputBytes:=0
	for {
		select{
		case ev:=<-events:
			switch ev.Kind{
			case goprotocol.OutputEvent:
				outputEvents++
				outputBytes+=len(ev.Data)
			case goprotocol.ExitEvent:
				if outputBytes<1024*1024{
					t.Fatalf("coalesced output retained only %d bytes",outputBytes)
				}
				if outputEvents>=512{
					t.Fatalf("1 MiB flood produced %d output events; tiny PTY reads were not coalesced",outputEvents)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-timer.C:
			t.Fatal("timed out waiting for coalesced terminal flood")
		}
	}
}

func TestResizeFollowsObservedOutputAndPrecedesLaterOutput(t *testing.T) {
	r:=NewRegistry()
	term,err:=r.Spawn(
		"/bin/sh",
		[]string{"-c","printf BEFORE_RESIZE; read _; printf AFTER_RESIZE"},
		goprotocol.TerminalSize{Columns:80,Lines:24},
	)
	if err!=nil{t.Fatal(err)}
	defer r.CloseAll()

	events,done,cancel:=term.Subscribe()
	defer cancel()
	timer:=time.NewTimer(5*time.Second)
	defer timer.Stop()

	var ordered []goprotocol.TerminalEvent
	beforeSeen:=false
	resized:=false
	for {
		select{
		case ev:=<-events:
			ordered=append(ordered,ev)
			if ev.Kind==goprotocol.OutputEvent && bytes.Contains(ev.Data,[]byte("BEFORE_RESIZE")) && !beforeSeen{
				beforeSeen=true
				if err:=term.Resize(goprotocol.TerminalSize{Columns:100,Lines:31});err!=nil{t.Fatal(err)}
				resized=true
				if err:=term.Write([]byte("\n"));err!=nil{t.Fatal(err)}
			}
			if ev.Kind==goprotocol.ExitEvent{
				if !beforeSeen||!resized{t.Fatal("terminal exited before resize sequence completed")}
				beforeIndex,resizeIndex,afterIndex,exitIndex:=-1,-1,-1,-1
				for i,item:=range ordered{
					switch item.Kind{
					case goprotocol.OutputEvent:
						if beforeIndex<0 && bytes.Contains(item.Data,[]byte("BEFORE_RESIZE")){beforeIndex=i}
						if afterIndex<0 && bytes.Contains(item.Data,[]byte("AFTER_RESIZE")){afterIndex=i}
					case goprotocol.ResizeEvent:
						if item.Size.Columns==100&&item.Size.Lines==31&&resizeIndex<0{resizeIndex=i}
					case goprotocol.ExitEvent:
						exitIndex=i
					}
				}
				if !(beforeIndex>=0&&beforeIndex<resizeIndex&&resizeIndex<afterIndex&&afterIndex<exitIndex){
					t.Fatalf("stream order before=%d resize=%d after=%d exit=%d events=%#v",beforeIndex,resizeIndex,afterIndex,exitIndex,ordered)
				}
				return
			}
		case <-done:
			t.Fatal("subscription ended before exit")
		case <-timer.C:
			t.Fatal("timed out waiting for resize ordering")
		}
	}
}
