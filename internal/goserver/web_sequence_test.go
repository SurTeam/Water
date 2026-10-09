package goserver

import (
	"encoding/json"
	"math"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
)

func TestWebReplayPreservesFullUint64Sequence(t *testing.T) {
	seq := uint64(math.MaxUint64)
	payload := webAttachPayload(terminalAttachResponse{FirstSeq: &seq, LastSeq: seq, Replay: []goprotocol.WireTerminalEvent{{Type: "output", Seq: seq, Bytes: "YQ=="}}})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		First  string `json:"first_seq"`
		Last   string `json:"last_seq"`
		Replay []struct {
			Seq string `json:"seq"`
		} `json:"replay"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{response.First, response.Last, response.Replay[0].Seq} {
		if value != "18446744073709551615" {
			t.Fatalf("sequence truncated: %s", raw)
		}
	}
}
func TestWebBrowserReleaseCannotBypassFocusOwnership(t *testing.T) {
	s, client, origin := webFixture(t)
	webPair(t, s, client, origin)
	c := webSocket(t, client, origin)
	openWeb(t, s, c)
	if s.uiSessionCount() != 0 || s.firstUISession() != nil || len(s.windowSessions()) != 0 {
		t.Fatal("browser incorrectly registered as an automatable desktop GUI")
	}
	serverSide, cliSide := net.Pipe()
	go s.handleConn(serverSide)
	defer cliSide.Close()
	listing := webCall(t, s, cliSide, 3, "connection.list", map[string]any{})
	if listing.OK == nil || !*listing.OK {
		t.Fatal("browser prevented headless CLI connection listing")
	}
	msg := webCall(t, s, c, 2, "session.release", map[string]bool{"shutdown_if_last": true})
	if msg.OK == nil || !*msg.OK {
		t.Fatal(msg.Error)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	for {
		_, err := goprotocol.ReadFrame(c)
		if err != nil {
			if e, ok := err.(net.Error); ok && e.Timeout() {
				t.Fatal("released browser connection stayed open")
			}
			break
		}
	}
	response, err := client.Get(origin + "/api/session")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatal("browser release closed server or revoked device")
	}
}
