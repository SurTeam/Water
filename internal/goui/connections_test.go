package goui

import (
	"testing"

	"github.com/SurTeam/Water/internal/goconfig"
	"github.com/google/uuid"
)

func TestMultiWorkspaceClientSwitchesConnectionsAndClosesResources(t *testing.T){
	manager:=NewMultiWorkspaceClient(nil)
	firstID:=uuid.MustParse("00000000-0000-0000-0000-000000000001")
	secondID:=uuid.MustParse("00000000-0000-0000-0000-000000000002")

	first:=NewWorkspaceClientWithConnection(nil,nil,goconfig.Default(),"")
	second:=NewWorkspaceClientWithConnection(nil,nil,goconfig.Default(),"example-host")
	closed:=0

	if err:=manager.AddConnection(ConnectionEntry{
		ID:firstID,Name:"Local",Kind:"local",Status:"connected",SocketPath:"/tmp/local.sock",
	},first,func(){closed++},true);err!=nil{t.Fatal(err)}
	if err:=manager.AddConnection(ConnectionEntry{
		ID:secondID,Name:"example-host",Kind:"remote",Status:"connected",
		SocketPath:"/tmp/remote.sock",RemoteSocketPath:"/tmp/server.sock",Destination:"example-host",
	},second,func(){closed++},false);err!=nil{t.Fatal(err)}

	if got:=manager.ActiveConnectionID();got!=firstID{
		t.Fatalf("active connection = %s, want %s",got,firstID)
	}
	if !manager.ActivateConnection(secondID){t.Fatal("could not activate second connection")}
	if got:=manager.ActiveConnectionID();got!=secondID{
		t.Fatalf("active connection = %s, want %s",got,secondID)
	}

	entries:=manager.ConnectionEntries()
	if len(entries)!=2{t.Fatalf("connection count = %d",len(entries))}
	firstList:=first.connectionListResponse()["connections"].([]map[string]any)
	secondList:=second.connectionListResponse()["connections"].([]map[string]any)
	if len(firstList)!=2 || len(secondList)!=2{
		t.Fatalf("child projections differ: %d %d",len(firstList),len(secondList))
	}
	for _,list:=range [][]map[string]any{firstList,secondList}{
		active:=0
		for _,entry:=range list{
			if value,ok:=entry["active"].(bool);ok&&value{active++}
		}
		if active!=1{t.Fatalf("active projection count = %d",active)}
	}

	if !manager.RemoveConnection(secondID){t.Fatal("remove remote failed")}
	if got:=manager.ActiveConnectionID();got!=firstID{
		t.Fatalf("active connection after removal = %s",got)
	}
	if closed!=1{t.Fatalf("close count after removal = %d",closed)}

	manager.Close()
	if closed!=2{t.Fatalf("close count after manager close = %d",closed)}
	manager.Close()
	if closed!=2{t.Fatalf("second close changed count to %d",closed)}
}

func TestMultiWorkspaceClientRejectsDuplicateID(t *testing.T){
	manager:=NewMultiWorkspaceClient(nil)
	defer manager.Close()
	id:=uuid.New()
	first:=NewWorkspaceClientWithConnection(nil,nil,goconfig.Default(),"")
	second:=NewWorkspaceClientWithConnection(nil,nil,goconfig.Default(),"")
	if err:=manager.AddConnection(ConnectionEntry{ID:id,Name:"one"},first,nil,true);err!=nil{t.Fatal(err)}
	closed:=0
	if err:=manager.AddConnection(ConnectionEntry{ID:id,Name:"two"},second,func(){closed++},false);err==nil{
		t.Fatal("duplicate connection ID unexpectedly succeeded")
	}
	if closed!=1{t.Fatalf("duplicate close count = %d",closed)}
}
