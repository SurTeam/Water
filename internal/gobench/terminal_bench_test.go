package gobench

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

const (
	benchBytes = 64_000_000
	multiPaneCount = 4
	multiPaneBytes = benchBytes / multiPaneCount
	visibleInterval = 16 * time.Millisecond
)

func terminalBenchmarkCommand() string {
	if command:=os.Getenv("WATER_GO_BENCH_COMMAND");command!="" { return command }
	return "yes WATER_GO_BENCH | head -c 64000000"
}

func BenchmarkTerminalDirect64MB(b *testing.B) {
	for i:=0;i<b.N;i++{
		r:=goterminal.NewRegistry()
		term,err:=r.Spawn("/bin/sh",[]string{"-c","read _; "+terminalBenchmarkCommand()},goprotocol.TerminalSize{Columns:80,Lines:24})
		if err!=nil{b.Fatal(err)}
		events,done,cancel:=term.Subscribe()
		emu:=govt.New(80,24,10_000)
		if err:=term.Write([]byte("\n"));err!=nil{b.Fatal(err)}
		var total int64
		start:=time.Now()
		timer:=time.NewTimer(30*time.Second)
	loop:
		for{
			select{
			case ev:=<-events:
				switch ev.Kind{
				case goprotocol.OutputEvent:
					total+=int64(len(ev.Data));emu.Write(ev.Data)
				case goprotocol.ResizeEvent:
					emu.Resize(ev.Size.Columns,ev.Size.Lines)
				case goprotocol.ExitEvent:
					break loop
				}
			case <-done:
				timer.Stop()
				b.Fatal("terminal subscription ended before exit")
			case <-timer.C:
				b.Fatal("terminal benchmark timed out")
			}
		}
		if !timer.Stop(){
			select{case <-timer.C:default:}
		}
		_ = emu.Snapshot()
		b.ReportMetric(float64(total)/time.Since(start).Seconds()/1e6,"MB/s")
		emu.Close();cancel();r.CloseAll()
	}
	b.SetBytes(benchBytes)
}

func BenchmarkTerminalServer64MB(b *testing.B) {
	for i:=0;i<b.N;i++{
		socket:=filepath.Join("/tmp","water-go-bench-"+uuid.New().String()+".sock")
		server:=goserver.New(socket)
		serveDone:=make(chan error,1)
		go func(){serveDone<-server.ListenAndServe()}()
		client:=goclient.New(socket)
		waitServer(b,client)

		var spawned struct{TerminalID uuid.UUID `json:"terminal_id"`}
		if err:=client.Dispatch(map[string]any{
			"type":"terminal.spawn","program":"/bin/sh",
			"args":[]string{"-c","read _; "+terminalBenchmarkCommand()},
			"columns":80,"lines":24,
		},&spawned);err!=nil{b.Fatal(err)}
		session,err:=client.OpenSession();if err!=nil{b.Fatal(err)}
		var attached struct{
			Size goprotocol.TerminalSize `json:"size"`
			Replay []goprotocol.WireTerminalEvent `json:"replay"`
			LastSeq uint64 `json:"last_seq"`
		}
		if err:=session.Attach(spawned.TerminalID,&attached);err!=nil{b.Fatal(err)}
		emu:=govt.New(attached.Size.Columns,attached.Size.Lines,10_000)
		lastSeq:=attached.LastSeq
		if err:=session.DispatchAsync(map[string]any{
			"type":"terminal.send_text","terminal_id":spawned.TerminalID,"text":"\n",
		});err!=nil{b.Fatal(err)}

		var total int64
		start:=time.Now()
		timer:=time.NewTimer(30*time.Second)
	loop:
		for{
			select{
			case push:=<-session.Events:
				if push.TerminalID!=spawned.TerminalID || push.Event.Seq<=lastSeq{continue}
				lastSeq=push.Event.Seq
				switch push.Event.Kind{
				case goprotocol.OutputEvent:
					total+=int64(len(push.Event.Data));emu.Write(push.Event.Data)
				case goprotocol.ResizeEvent:
					emu.Resize(push.Event.Size.Columns,push.Event.Size.Lines)
				case goprotocol.ExitEvent:
					break loop
				}
			case <-timer.C:
				b.Fatal("server terminal benchmark timed out")
			}
		}
		if !timer.Stop(){
			select{case <-timer.C:default:}
		}
		_ = emu.Snapshot()
		b.ReportMetric(float64(total)/time.Since(start).Seconds()/1e6,"MB/s")
		emu.Close();_ = session.Close();_ = server.Close()
		select{case <-serveDone:case <-time.After(time.Second):}
	}
	b.SetBytes(benchBytes)
}

func multiPaneBenchmarkCommand() string {
	if command:=os.Getenv("WATER_GO_MULTI_BENCH_COMMAND");command!="" { return command }
	return "yes WATER_GO_MULTI | head -c 16000000"
}

type multiPaneResult struct {
	bytes int64
	gaps []time.Duration
	emu *govt.Emulator
	complete bool
}

func BenchmarkTerminalDirect4Pane64MB(b *testing.B) {
	for i:=0;i<b.N;i++{
		registry:=goterminal.NewRegistry()
		results:=make(chan multiPaneResult,multiPaneCount)
		terms:=make([]*goterminal.Terminal,0,multiPaneCount)
		cancels:=make([]func(),0,multiPaneCount)

		for pane:=0;pane<multiPaneCount;pane++{
			term,err:=registry.Spawn(
				"/bin/sh",
				[]string{"-c","read _; "+multiPaneBenchmarkCommand()},
				goprotocol.TerminalSize{Columns:80,Lines:24},
			)
			if err!=nil{b.Fatal(err)}
			events,done,cancel:=term.Subscribe()
			emu:=govt.New(80,24,10_000)
			terms=append(terms,term)
			cancels=append(cancels,cancel)
			go func(){
				results<-consumeMultiPaneDirect(events,done,emu)
			}()
		}

		start:=time.Now()
		for _,term:=range terms{
			if err:=term.Write([]byte("\n"));err!=nil{b.Fatal(err)}
		}
		var total int64
		var gaps []time.Duration
		emus:=make([]*govt.Emulator,0,multiPaneCount)
		for range multiPaneCount{
			result:=<-results
			if !result.complete{b.Fatal("multi-pane direct terminal ended before Exit")}
			total+=result.bytes
			gaps=append(gaps,result.gaps...)
			emus=append(emus,result.emu)
		}
		if total<int64(benchBytes){b.Fatalf("multi-pane direct bytes = %d, want at least %d",total,benchBytes)}
		duration:=time.Since(start)
		b.ReportMetric(float64(total)/duration.Seconds()/1e6,"MB/s")
		reportVisibleMetrics(b,gaps)
		if rss:=currentRSSKB();rss>0{b.ReportMetric(float64(rss),"rss-KB")}
		for _,emu:=range emus{emu.Close()}
		for _,cancel:=range cancels{cancel()}
		registry.CloseAll()
	}
	b.SetBytes(benchBytes)
}

func BenchmarkTerminalServer4Pane64MB(b *testing.B) {
	for i:=0;i<b.N;i++{
		socket:=filepath.Join("/tmp","water-go-multipane-"+uuid.New().String()+".sock")
		server:=goserver.New(socket)
		serveDone:=make(chan error,1)
		go func(){serveDone<-server.ListenAndServe()}()
		client:=goclient.New(socket)
		waitServer(b,client)

		session,err:=client.OpenSession()
		if err!=nil{b.Fatal(err)}
		ids:=make([]uuid.UUID,0,multiPaneCount)
		emus:=make(map[uuid.UUID]*govt.Emulator,multiPaneCount)
		lastSeq:=make(map[uuid.UUID]uint64,multiPaneCount)
		paneBytes:=make(map[uuid.UUID]int64,multiPaneCount)
		lastSnapshot:=make(map[uuid.UUID]time.Time,multiPaneCount)
		gaps:=make([]time.Duration,0,1024)

		if err:=client.Dispatch(map[string]any{"type":"workspace.new"},nil);err!=nil{b.Fatal(err)}
		var state gomodel.StateDump
		if err:=client.Call("state.dump",map[string]any{},&state);err!=nil{b.Fatal(err)}
		if state.FocusedPane==nil{b.Fatal("workspace.new did not create a focused pane")}
		root:=*state.FocusedPane
		split:=func(paneID uuid.UUID,direction string)uuid.UUID{
			var result struct{PaneID uuid.UUID `json:"pane_id"`}
			if err:=client.Dispatch(map[string]any{
				"type":"pane.split",
				"pane_id":paneID,
				"direction":direction,
			},&result);err!=nil{b.Fatal(err)}
			if result.PaneID==uuid.Nil{b.Fatal("pane.split returned nil pane id")}
			return result.PaneID
		}
		right:=split(root,"right")
		lowerLeft:=split(root,"down")
		lowerRight:=split(right,"down")
		panes:=[]uuid.UUID{root,right,lowerLeft,lowerRight}

		for _,paneID:=range panes{
			var spawned struct{TerminalID uuid.UUID `json:"terminal_id"`}
			if err:=client.Dispatch(map[string]any{
				"type":"terminal.spawn",
				"pane_id":paneID,
				"program":"/bin/sh",
				"args":[]string{"-c","read _; "+multiPaneBenchmarkCommand()},
				"columns":80,
				"lines":24,
			},&spawned);err!=nil{b.Fatal(err)}
			var attached struct{
				Size goprotocol.TerminalSize `json:"size"`
				LastSeq uint64 `json:"last_seq"`
			}
			if err:=session.Attach(spawned.TerminalID,&attached);err!=nil{b.Fatal(err)}
			ids=append(ids,spawned.TerminalID)
			emus[spawned.TerminalID]=govt.New(attached.Size.Columns,attached.Size.Lines,10_000)
			lastSeq[spawned.TerminalID]=attached.LastSeq
		}

		start:=time.Now()
		for _,id:=range ids{
			lastSnapshot[id]=start
			if err:=session.DispatchAsync(map[string]any{
				"type":"terminal.send_text",
				"terminal_id":id,
				"text":"\n",
			});err!=nil{b.Fatal(err)}
		}

		var total int64
		exited:=make(map[uuid.UUID]bool,multiPaneCount)
		timer:=time.NewTimer(30*time.Second)
		for len(exited)<multiPaneCount{
			select{
			case push,ok:=<-session.Events:
				if !ok{b.Fatal("multi-pane server stream closed")}
				emu:=emus[push.TerminalID]
				if emu==nil{continue}
				if push.Event.Seq<=lastSeq[push.TerminalID]{continue}
				if previous:=lastSeq[push.TerminalID];previous!=0 && push.Event.Seq!=previous+1{
					b.Fatalf("multi-pane server sequence gap terminal=%s previous=%d next=%d",push.TerminalID,previous,push.Event.Seq)
				}
				lastSeq[push.TerminalID]=push.Event.Seq
				switch push.Event.Kind{
				case goprotocol.OutputEvent:
					n:=int64(len(push.Event.Data))
					total+=n
					paneBytes[push.TerminalID]+=n
					emu.Write(push.Event.Data)
				case goprotocol.ResizeEvent:
					emu.Resize(push.Event.Size.Columns,push.Event.Size.Lines)
				case goprotocol.ExitEvent:
					if !exited[push.TerminalID]{
						exited[push.TerminalID]=true
						now:=time.Now()
						gaps=append(gaps,now.Sub(lastSnapshot[push.TerminalID]))
						lastSnapshot[push.TerminalID]=now
						_ = emu.Snapshot()
					}
				}
				if !exited[push.TerminalID] && time.Since(lastSnapshot[push.TerminalID])>=visibleInterval{
					now:=time.Now()
					gaps=append(gaps,now.Sub(lastSnapshot[push.TerminalID]))
					lastSnapshot[push.TerminalID]=now
					_ = emu.Snapshot()
				}
			case <-timer.C:
				b.Fatal("multi-pane server benchmark timed out")
			}
		}
		if !timer.Stop(){select{case <-timer.C:default:}}
		for _,id:=range ids{
			if paneBytes[id]<int64(multiPaneBytes){
				b.Fatalf("multi-pane server terminal %s bytes = %d, want at least %d",id,paneBytes[id],multiPaneBytes)
			}
		}
		if total<int64(benchBytes){b.Fatalf("multi-pane server bytes = %d, want at least %d",total,benchBytes)}
		duration:=time.Since(start)
		b.ReportMetric(float64(total)/duration.Seconds()/1e6,"MB/s")
		reportVisibleMetrics(b,gaps)
		if rss:=currentRSSKB();rss>0{b.ReportMetric(float64(rss),"rss-KB")}

		for _,emu:=range emus{emu.Close()}
		_ = session.Close()
		_ = server.Close()
		select{case <-serveDone:case <-time.After(time.Second):}
	}
	b.SetBytes(benchBytes)
}

func consumeMultiPaneDirect(
	events <-chan goprotocol.TerminalEvent,
	done <-chan struct{},
	emu *govt.Emulator,
) multiPaneResult {
	lastSnapshot:=time.Now()
	var total int64
	gaps:=make([]time.Duration,0,256)
	timer:=time.NewTimer(30*time.Second)
	defer timer.Stop()
	for{
		select{
		case ev:=<-events:
			switch ev.Kind{
			case goprotocol.OutputEvent:
				total+=int64(len(ev.Data))
				emu.Write(ev.Data)
			case goprotocol.ResizeEvent:
				emu.Resize(ev.Size.Columns,ev.Size.Lines)
			case goprotocol.ExitEvent:
				now:=time.Now()
				gaps=append(gaps,now.Sub(lastSnapshot))
				_ = emu.Snapshot()
				return multiPaneResult{bytes:total,gaps:gaps,emu:emu,complete:true}
			}
			if time.Since(lastSnapshot)>=visibleInterval{
				now:=time.Now()
				gaps=append(gaps,now.Sub(lastSnapshot))
				lastSnapshot=now
				_ = emu.Snapshot()
			}
		case <-done:
			return multiPaneResult{bytes:total,gaps:gaps,emu:emu,complete:false}
		case <-timer.C:
			return multiPaneResult{bytes:total,gaps:gaps,emu:emu,complete:false}
		}
	}
}

func reportVisibleMetrics(b *testing.B,gaps []time.Duration){
	if len(gaps)==0{return}
	b.ReportMetric(float64(percentileDuration(gaps,0.95))/float64(time.Millisecond),"frame-p95-ms")
	b.ReportMetric(float64(percentileDuration(gaps,0.99))/float64(time.Millisecond),"frame-p99-ms")
}

func percentileDuration(values []time.Duration,p float64)time.Duration{
	if len(values)==0{return 0}
	copyValues:=append([]time.Duration(nil),values...)
	sort.Slice(copyValues,func(i,j int)bool{return copyValues[i]<copyValues[j]})
	index:=int(float64(len(copyValues)-1)*p+0.999999)
	if index<0{index=0}
	if index>=len(copyValues){index=len(copyValues)-1}
	return copyValues[index]
}

func currentRSSKB()uint64{
	if runtime.GOOS!="linux"{return 0}
	data,err:=os.ReadFile("/proc/self/statm")
	if err!=nil{return 0}
	fields:=strings.Fields(string(data))
	if len(fields)<2{return 0}
	pages,err:=strconv.ParseUint(fields[1],10,64)
	if err!=nil{return 0}
	return pages*uint64(os.Getpagesize())/1024
}

func waitServer(b *testing.B,client *goclient.Client){
	deadline:=time.Now().Add(2*time.Second)
	for{
		var out map[string]any
		if client.Call("ping",map[string]any{},&out)==nil{return}
		if time.Now().After(deadline){b.Fatal("server did not become ready")}
		time.Sleep(5*time.Millisecond)
	}
}
