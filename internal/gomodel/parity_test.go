package gomodel

import (
	"testing"

	"github.com/SurTeam/Water/internal/goagent"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

func TestDirectionalFocusUsesSplitGeometry(t *testing.T){
	m:=New()
	wid:=m.CreateWorkspace("w")
	tab,left,err:=m.CreateTab(wid,"t",false)
	if err!=nil{t.Fatal(err)}
	right,err:=m.SplitPane(left,"right")
	if err!=nil{t.Fatal(err)}
	downRight,err:=m.SplitPane(right,"down")
	if err!=nil{t.Fatal(err)}

	if got,ok:=m.DirectionalPane(tab,left,"right");!ok || (got!=right && got!=downRight){
		t.Fatalf("right neighbor=%s ok=%v",got,ok)
	}
	if got,ok:=m.DirectionalPane(tab,downRight,"left");!ok || got!=left{
		t.Fatalf("left neighbor=%s ok=%v want %s",got,ok,left)
	}
}

func TestPromoteAndMovePanePreserveTerminal(t *testing.T){
	m:=New()
	w1:=m.CreateWorkspace("one")
	tab,p1,err:=m.CreateTab(w1,"tab",false)
	if err!=nil{t.Fatal(err)}
	_ = tab
	p2,err:=m.SplitPane(p1,"right")
	if err!=nil{t.Fatal(err)}
	termID:=uuid.New()
	if err:=m.InstallTerminal(p2,TerminalMeta{
		TerminalID:termID,
		SessionID:uuid.New(),
		Program:"codex",
		ProcessName:"codex",
		Size:goprotocol.TerminalSize{Columns:80,Lines:24},
		Agent:&goagent.DetectedAgent{Kind:goagent.Codex},
	});err!=nil{t.Fatal(err)}

	move,moved,err:=m.PromotePaneToTab(p2)
	if err!=nil || !moved{t.Fatalf("promote: moved=%v err=%v",moved,err)}
	if move.TargetTabID==uuid.Nil{t.Fatal("missing promoted tab")}
	if got,ok:=m.TerminalForPane(p2);!ok || got!=termID{
		t.Fatalf("terminal changed after promote: %s %v",got,ok)
	}

	w2:=m.CreateWorkspace("two")
	move,moved,err=m.MovePaneToWorkspace(p2,w2)
	if err!=nil || !moved{t.Fatalf("move: moved=%v err=%v",moved,err)}
	if move.TargetTabID==uuid.Nil{t.Fatal("missing moved tab")}
	if got,ok:=m.TerminalForPane(p2);!ok || got!=termID{
		t.Fatalf("terminal changed after move: %s %v",got,ok)
	}
}

func TestReplaceSurfaceAndAgentRename(t *testing.T){
	m:=New()
	w:=m.CreateWorkspace("w")
	_,pane,err:=m.CreateTab(w,"t",false)
	if err!=nil{t.Fatal(err)}
	term:=uuid.New()
	if err:=m.InstallTerminal(pane,TerminalMeta{
		TerminalID:term,
		SessionID:uuid.New(),
		Program:"codex",
		ProcessName:"codex",
		Size:goprotocol.TerminalSize{Columns:80,Lines:24},
		Agent:&goagent.DetectedAgent{Kind:goagent.Codex},
	});err!=nil{t.Fatal(err)}
	if err:=m.RenameAgent(pane," Fix auth ");err!=nil{t.Fatal(err)}
	dump:=m.Dump()
	if len(dump.Agents)!=1{t.Fatalf("agents=%#v",dump.Agents)}
	_,old,err:=m.ReplaceSurfaceEmpty(pane)
	if err!=nil{t.Fatal(err)}
	if old==nil || *old!=term{t.Fatalf("old terminal=%v",old)}
	if _,ok:=m.TerminalForPane(pane);ok{t.Fatal("terminal survived empty replacement")}
}
