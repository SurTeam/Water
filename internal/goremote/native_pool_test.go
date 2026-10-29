package goremote

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestNativeSSHLeaseOwnership(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	server := &ssh.ServerConfig{NoClientAuth: true}
	server.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var accepted atomic.Int32
	go func() {
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			go func() {
				transport, channels, requests, err := ssh.NewServerConn(connection, server)
				if err != nil {
					connection.Close()
					return
				}
				defer transport.Close()
				go ssh.DiscardRequests(requests)
				for channel := range channels {
					channel.Reject(ssh.UnknownChannelType, "fixture has no sessions")
				}
			}()
		}
	}()
	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	known := filepath.Join(directory, "known_hosts")
	if err := os.WriteFile(known, []byte(knownhosts.Line([]string{listener.Addr().String()}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(directory, "config")
	text := fmt.Sprintf("Host lease-fixture\n HostName %s\n Port %s\n User fixture\n IdentityFile none\n IdentitiesOnly yes\n UserKnownHostsFile \"%s\"\n StrictHostKeyChecking yes\n", host, port, filepath.ToSlash(known))
	if err := os.WriteFile(config, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WATER_SSH_CONFIG", config)
	var workers sync.WaitGroup
	clients := make(chan *nativeSSH, 8)
	releases := make(chan func(), 8)
	failures := make(chan error, 8)
	for range 8 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			client, release, err := acquireNativeSSH("lease-fixture", nil)
			if err != nil {
				failures <- err
				return
			}
			clients <- client
			releases <- release
		}()
	}
	workers.Wait()
	close(clients)
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if t.Failed() {
		return
	}
	var first *nativeSSH
	for client := range clients {
		if first == nil {
			first = client
			defer first.close()
		}
		if client != first {
			t.Fatal("concurrent views did not reuse one authenticated connection")
		}
	}
	for range 8 {
		(<-releases)()
	}
	replacement, release, err := acquireNativeSSH("lease-fixture", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if replacement != first || accepted.Load() != 1 {
		t.Fatal("closing views discarded authenticated transport")
	}
}
