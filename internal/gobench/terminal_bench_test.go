package gobench

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goclient"
	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/SurTeam/Water/internal/goserver"
	"github.com/SurTeam/Water/internal/goterminal"
	"github.com/SurTeam/Water/internal/govt"
	"github.com/google/uuid"
)

const benchBytes = 64_000_000

func BenchmarkTerminalDirect64MB(b *testing.B) {
	for i:=0;i<b.N;i++{
		r:=goterminal.NewRegistry()
		term,err:=r.Spawn("/bin/sh",[]string{"-c","read _; yes WATER_GO_BENCH | head -c 64000000"},goprotocol.TerminalSize{Columns:80,Lines:24})
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
			"args":[]string{"-c","read _; yes WATER_GO_BENCH | head -c 64000000"},
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

func waitServer(b *testing.B,client *goclient.Client){
	deadline:=time.Now().Add(2*time.Second)
	for{
		var out map[string]any
		if client.Call("ping",map[string]any{},&out)==nil{return}
		if time.Now().After(deadline){b.Fatal("server did not become ready")}
		time.Sleep(5*time.Millisecond)
	}
}
