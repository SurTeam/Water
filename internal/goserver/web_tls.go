package goserver

import (
	"crypto/tls"
	"errors"
	"os"
	"path/filepath"

	"github.com/SurTeam/Water/internal/goconfig"
)

const maxWebTLSFile = 256 << 10

// Import a complete pair into a new private directory. Never overwrite the
// running listener's credentials or interpret desktop paths on another host.
func (w *webService) configureFiles(cfg goconfig.WebConfig, certificate, key []byte) error {
	if len(certificate) == 0 && len(key) == 0 {
		return w.configure(cfg)
	}
	if len(certificate) == 0 || len(key) == 0 {
		return errors.New("select both certificate and private key files")
	}
	if len(certificate) > maxWebTLSFile || len(key) > maxWebTLSFile {
		return errors.New("TLS files must be at most 256 KiB each")
	}
	if _, err := tls.X509KeyPair(certificate, key); err != nil {
		return errors.New("invalid TLS pair: use PEM certificates and a matching unencrypted PEM private key")
	}
	base := w.server.SocketPath + ".tls-" + w.server.Build
	if w.server.ConfigPath != "" {
		base = filepath.Join(filepath.Dir(w.server.ConfigPath), "web-tls-"+w.server.Build)
	}
	if err := os.MkdirAll(base, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(base)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("TLS storage directory must be private (0700)")
	}
	dir, err := os.MkdirTemp(base, "pair-")
	if err != nil {
		return err
	}
	saved := false
	defer func() {
		if !saved {
			_ = os.RemoveAll(dir)
		}
	}()
	cfg.TLSCertFile = filepath.Join(dir, "fullchain.pem")
	cfg.TLSKeyFile = filepath.Join(dir, "private-key.pem")
	if err = os.WriteFile(cfg.TLSCertFile, certificate, 0600); err != nil {
		return err
	}
	if err = os.WriteFile(cfg.TLSKeyFile, key, 0600); err != nil {
		return err
	}
	if err = w.configure(cfg); err != nil {
		return err
	}
	saved = true
	return nil
}
