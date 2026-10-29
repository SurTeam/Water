package gobridge

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"testing"
	"time"
)

func TestRelayPreservesBinaryAndFinalOutput(t *testing.T) {
	local, client := net.Pipe()
	remote, server := net.Pipe()
	leftIn, rightOut := io.Pipe()
	rightIn, leftOut := io.Pipe()
	done := make(chan error, 2)
	go func() { done <- Relay(local, leftIn, leftOut) }()
	go func() { done <- Relay(remote, rightIn, rightOut) }()
	payload := bytes.Repeat([]byte{0, 0xff, '\r', '\n', 0x1b, 0xc3, 0xa9}, 30000)
	go func() {
		defer server.Close()
		data := make([]byte, len(payload))
		_, err := io.ReadFull(server, data)
		if err == nil && bytes.Equal(data, payload) {
			server.Write(data)
		}
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	go func() { client.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(client, got); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("binary stream changed")
	}
	client.Close()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("relay did not stop")
		}
	}
}
func TestDecoderToleratesOnlyBoundedStartupBanner(t *testing.T) {
	valid := framePrefix + base64.StdEncoding.EncodeToString([]byte("payload")) + "\n"
	var got bytes.Buffer
	if err := decode(&got, bytes.NewBufferString("Using Node for alias default\n"+valid)); err != io.EOF || got.String() != "payload" {
		t.Fatalf("banner relay: output=%q error=%v", got.String(), err)
	}
	for _, input := range []string{valid + "bad frame\n", string(bytes.Repeat([]byte("x"), 8193)) + "\n" + valid} {
		if err := decode(io.Discard, bytes.NewBufferString(input)); err == nil || err == io.EOF {
			t.Fatalf("accepted corrupted bridge: %v", err)
		}
	}
}

func TestDecoderRejectsInvalidAndOversizedFrames(t *testing.T) {
	for _, data := range []string{"not base64\n", base64.StdEncoding.EncodeToString(make([]byte, BlockSize+1)) + "\n"} {
		if err := decode(io.Discard, bytes.NewBufferString(data)); err == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}
