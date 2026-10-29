package goremote

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// Keep authenticated transports briefly, like Unix ControlPersist. Replacing a
// GUI view can reconnect without retaining passwords or prompting in background.
type nativeLease struct {
	client     *nativeSSH
	err        error
	ready      chan struct{}
	references int
	timer      *time.Timer
}

var nativeConnections = struct {
	sync.Mutex
	entries map[string]*nativeLease
}{entries: map[string]*nativeLease{}}

func acquireNativeSSH(destination string, prompt Prompt) (*nativeSSH, func(), error) {
	configuration, err := loadNativeConfig(destination)
	if err != nil {
		return nil, nil, err
	}
	identity, _ := json.Marshal([]any{destination, configuration.host, configuration.port, configuration.user, configuration.values, configuration.identities})
	key := fmt.Sprintf("%x", sha256.Sum256(identity))
	nativeConnections.Lock()
	entry, exists := nativeConnections.entries[key]
	if exists {
		entry.references++
		if entry.timer != nil {
			entry.timer.Stop()
			entry.timer = nil
		}
		nativeConnections.Unlock()
		<-entry.ready
	} else {
		entry = &nativeLease{ready: make(chan struct{}), references: 1}
		nativeConnections.entries[key] = entry
		nativeConnections.Unlock()
		entry.client, entry.err = dialNativeSSH(destination, prompt, 0)
		nativeConnections.Lock()
		if entry.err != nil {
			delete(nativeConnections.entries, key)
		}
		close(entry.ready)
		nativeConnections.Unlock()
		if entry.err == nil {
			go func() {
				_ = entry.client.client.Wait()
				nativeConnections.Lock()
				if nativeConnections.entries[key] == entry {
					delete(nativeConnections.entries, key)
				}
				if entry.timer != nil {
					entry.timer.Stop()
				}
				nativeConnections.Unlock()
				entry.client.close()
			}()
		}
	}
	if entry.err != nil {
		return nil, nil, entry.err
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			nativeConnections.Lock()
			entry.references--
			if entry.references == 0 {
				entry.timer = time.AfterFunc(10*time.Minute, func() {
					nativeConnections.Lock()
					if entry.references == 0 && nativeConnections.entries[key] == entry {
						delete(nativeConnections.entries, key)
						nativeConnections.Unlock()
						entry.client.close()
						return
					}
					nativeConnections.Unlock()
				})
			}
			nativeConnections.Unlock()
		})
	}
	return entry.client, release, nil
}
