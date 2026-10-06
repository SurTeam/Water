package goclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/SurTeam/Water/internal/gomodel"
	"github.com/google/uuid"
)

type RecoveryArtifact struct {
	Variant  string                 `json:"variant"`
	Endpoint string                 `json:"endpoint"`
	Pending  bool                   `json:"pending"`
	Layout   gomodel.RecoveryLayout `json:"layout"`
}
type RecoveryPrepared struct {
	Token  uuid.UUID              `json:"token"`
	Layout gomodel.RecoveryLayout `json:"layout"`
}

func RecoveryPath(configPath, variant, endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return filepath.Join(filepath.Dir(configPath), "recovery", variant+"-"+hex.EncodeToString(sum[:16])+".json")
}
func LoadRecovery(path, variant, endpoint string) (RecoveryArtifact, error) {
	var a RecoveryArtifact
	f, err := os.Open(path)
	if err != nil {
		return a, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return a, err
	}
	if stat.Size() > 16<<20 {
		return a, errors.New("recovery artifact exceeds size limit")
	}
	err = json.NewDecoder(f).Decode(&a)
	if err != nil {
		return a, err
	}
	if a.Variant != variant || a.Endpoint != endpoint {
		return a, errors.New("recovery artifact belongs to another connection or variant")
	}
	return a, a.Layout.Validate()
}
func SaveRecovery(path string, a RecoveryArtifact) error {
	if err := a.Layout.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = json.NewEncoder(f).Encode(a); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func PrepareRecovery(s *Session, path, variant, endpoint string, pending bool, project ...func(*gomodel.RecoveryLayout)) (RecoveryPrepared, error) {
	var p RecoveryPrepared
	if a, err := LoadRecovery(path, variant, endpoint); err == nil {
		if a.Pending && a.Layout.SourceInstance != s.Server.InstanceID {
			return p, errors.New("a pending recovery exists; restore it before creating another backup")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return p, fmt.Errorf("existing recovery artifact is invalid; left untouched: %w", err)
	}
	if err := s.CallTimeout("recovery.prepare", map[string]any{"expected_instance": s.Server.InstanceID}, &p, 15*time.Second); err != nil {
		return p, err
	}
	if len(project) > 0 && project[0] != nil {
		project[0](&p.Layout)
	}
	cancel := func() { _ = s.CallTimeout("recovery.cancel", map[string]any{"token": p.Token}, nil, time.Second) }
	if err := SaveRecovery(path, RecoveryArtifact{Variant: variant, Endpoint: endpoint, Pending: pending, Layout: p.Layout}); err != nil {
		cancel()
		return p, fmt.Errorf("save recovery; server left running: %w", err)
	}
	if !pending {
		cancel()
	}
	return p, nil
}
func RestorePending(s *Session, path, variant, endpoint string) error {
	a, err := LoadRecovery(path, variant, endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !a.Pending || a.Layout.SourceInstance == s.Server.InstanceID {
		return nil
	}
	if s.Diagnostic {
		return errors.New("replacement server is incompatible; saved recovery remains available")
	}
	if err := s.Dispatch(map[string]any{"type": "recovery.restore", "layout": a.Layout}, nil); err != nil {
		return fmt.Errorf("restore saved layout %s: %w", path, err)
	}
	a.Pending = false
	if err := SaveRecovery(path, a); err != nil {
		return fmt.Errorf("layout restored but recovery completion could not be saved: %w", err)
	}
	return nil
}

// WaitForShutdown observes the source instance disappearing; it never removes
// sockets or kills a process. A different listener is an explicit conflict.
func WaitForShutdown(client *Client, instance string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, err := client.Inspect(250 * time.Millisecond)
		if err != nil {
			// The listener file is removed only after owned socket cleanup completes.
			if _, err := os.Stat(client.SocketPath); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		} else if info.InstanceID != instance {
			return errors.New("another server took ownership of the socket during restart")
		}
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("server shutdown timed out; saved recovery remains available")
}
