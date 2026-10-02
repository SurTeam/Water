package goserver

import (
	"testing"
	"time"
)

func TestBroadcastSnapshotCoalescesWithoutWaitingForSessionWriter(t *testing.T){
	s:=New("")
	ss:=&session{
		snapshotRequests:make(chan struct{},1),
		done:make(chan struct{}),
	}
	s.sessions[ss]=struct{}{}

	// Simulate a session writer that is already blocked on an unrelated frame.
	// Snapshot publication must remain latest-wins and never join that wait.
	ss.mu.Lock()
	defer ss.mu.Unlock()

	done:=make(chan struct{})
	go func(){
		for i:=0;i<100;i++{
			s.broadcastSnapshot()
		}
		close(done)
	}()

	select{
	case <-done:
	case <-time.After(100*time.Millisecond):
		t.Fatal("broadcastSnapshot blocked behind the session writer")
	}
	if got:=len(ss.snapshotRequests);got!=1{
		t.Fatalf("pending snapshot notifications = %d, want exactly one coalesced request",got)
	}
}
