package goserver

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SurTeam/Water/internal/goprotocol"
	"github.com/google/uuid"
)

const webDeviceLifetime = 30 * 24 * time.Hour

type webStoredDevice struct {
	goprotocol.WebDevice
	Digest string `json:"digest"`
}
type webAuthStore struct {
	ID      uuid.UUID         `json:"id"`
	Variant string            `json:"variant"`
	Devices []webStoredDevice `json:"devices"`
}
type webInvite struct {
	id      uuid.UUID
	digest  string
	expires time.Time
}

func webSecret() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func webDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// The private directory and atomic rename prevent partial authorization writes.
// Configured servers retain identity when a control socket changes on restart.
func (w *webService) loadAuthLocked() error {
	if w.auth.ID != uuid.Nil {
		return nil
	}
	dir := w.server.SocketPath + ".web-" + w.server.Build
	if w.server.ConfigPath != "" {
		configPath, err := filepath.Abs(w.server.ConfigPath)
		if err != nil {
			return err
		}
		dir = filepath.Join(filepath.Dir(configPath), "web-"+w.server.Build+"-config-"+webDigest(configPath))
		if err := os.MkdirAll(filepath.Dir(dir), 0700); err != nil {
			return err
		}
		legacy := filepath.Join(filepath.Dir(configPath), "web-"+w.server.Build+"-"+webDigest(w.server.SocketPath))
		if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
			if info, oldErr := os.Lstat(legacy); oldErr == nil {
				if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
					return errors.New("legacy Web authorization directory must be private (0700)")
				}
				if err := os.Rename(legacy, dir); err != nil {
					return fmt.Errorf("migrate Web authorization: %w", err)
				}
			} else if !errors.Is(oldErr, os.ErrNotExist) {
				return oldErr
			}
		} else if err != nil {
			return err
		}
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("Web authorization directory must be a private directory (0700)")
	}
	w.authPath = filepath.Join(dir, "devices.json")
	info, err = os.Lstat(w.authPath)
	if errors.Is(err, os.ErrNotExist) {
		w.auth = webAuthStore{ID: uuid.New(), Variant: w.server.Build, Devices: []webStoredDevice{}}
		return w.saveAuthLocked()
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1024*1024 {
		return errors.New("invalid Web authorization file permissions or size")
	}
	data, err := os.ReadFile(w.authPath)
	if err != nil {
		return err
	}
	var store webAuthStore
	if err := json.Unmarshal(data, &store); err != nil {
		return fmt.Errorf("read Web authorization: %w", err)
	}
	if store.ID == uuid.Nil || store.Variant != w.server.Build || len(store.Devices) > 64 {
		return errors.New("invalid Web authorization identity")
	}
	w.auth = store
	return nil
}
func (w *webService) saveAuthLocked() error {
	data, err := json.Marshal(w.auth)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(w.authPath), ".devices-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), w.authPath)
}

func (w *webService) createInvite() (goprotocol.WebPairing, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.http == nil {
		return goprotocol.WebPairing{}, errors.New("start the Web service before pairing")
	}
	now := time.Now()
	for id, inv := range w.invites {
		if now.After(inv.expires) {
			delete(w.invites, id)
		}
	}
	if len(w.invites) >= 32 {
		return goprotocol.WebPairing{}, errors.New("too many pending pairing invitations")
	}
	token, err := webSecret()
	if err != nil {
		return goprotocol.WebPairing{}, err
	}
	inv := webInvite{id: uuid.New(), digest: webDigest(token), expires: now.Add(5 * time.Minute)}
	w.invites[inv.id] = inv
	return goprotocol.WebPairing{ID: inv.id.String(), URL: w.originLocked() + "/#pair=" + token, ExpiresAt: inv.expires}, nil
}

func (w *webService) exchangeInvite(token, name string) (string, error) {
	if len(token) != 43 {
		return "", errors.New("invalid or expired pairing invitation")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	digest := webDigest(token)
	var found *webInvite
	for id, inv := range w.invites {
		if now.After(inv.expires) {
			delete(w.invites, id)
			continue
		}
		if inv.digest == digest {
			v := inv
			found = &v
		}
	}
	if found == nil {
		return "", errors.New("invalid or expired pairing invitation")
	}
	// Prune expired authorizations before enforcing the persistent device limit.
	kept := make([]webStoredDevice, 0, len(w.auth.Devices))
	for _, d := range w.auth.Devices {
		if now.Before(d.ExpiresAt) {
			kept = append(kept, d)
		}
	}
	if len(kept) >= 64 {
		return "", errors.New("device limit reached; revoke an existing device")
	}
	secret, err := webSecret()
	if err != nil {
		return "", err
	}
	if len(name) > 80 {
		name = name[:80]
	}
	if name == "" {
		name = "Browser"
	}
	device := webStoredDevice{WebDevice: goprotocol.WebDevice{ID: uuid.NewString(), Name: name, CreatedAt: now, ExpiresAt: now.Add(webDeviceLifetime)}, Digest: webDigest(secret)}
	previous := w.auth.Devices
	w.auth.Devices = append(kept, device)
	if err := w.saveAuthLocked(); err != nil {
		w.auth.Devices = previous
		return "", err
	}
	delete(w.invites, found.id)
	return secret, nil
}
func (w *webService) deviceLocked(token string) (webStoredDevice, bool) {
	if len(token) != 43 {
		return webStoredDevice{}, false
	}
	digest := webDigest(token)
	for _, d := range w.auth.Devices {
		if d.Digest == digest && time.Now().Before(d.ExpiresAt) {
			return d, true
		}
	}
	return webStoredDevice{}, false
}
