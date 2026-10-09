package goui

import (
	"encoding/pem"
	"errors"
	"io"
	"os"
	"strings"
)

const maxWebTLSFile = 256 << 10

func readWebTLSFile(path string, private bool) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("cannot open selected TLS file")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("select a regular PEM file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxWebTLSFile+1))
	if err != nil || len(data) > maxWebTLSFile {
		return nil, errors.New("TLS files must be at most 256 KiB each")
	}
	block, _ := pem.Decode(data)
	valid := block != nil
	if valid && private {
		valid = (block.Type == "PRIVATE KEY" || block.Type == "RSA PRIVATE KEY" || block.Type == "EC PRIVATE KEY") && len(block.Headers) == 0
	}
	if valid && !private {
		valid = block.Type == "CERTIFICATE"
	}
	if !valid {
		return nil, errors.New("use PEM certificates and an unencrypted PEM private key")
	}
	return data, nil
}

// Called under layoutMu; only the picker and read run on the worker. The local
// path is a draft label, never a target-server path until validated import.
func (c *WorkspaceClient) selectWebTLSFile(action string) {
	c.server.mu.Lock()
	if c.server.busy {
		c.server.mu.Unlock()
		return
	}
	picker := c.server.chooseFile
	if picker == nil {
		c.server.message = "File picker unavailable"
		c.server.mu.Unlock()
		return
	}
	c.server.busy = true
	c.server.message = "Choose a file on this computer"
	generation := c.server.pairGeneration
	c.server.mu.Unlock()
	private := strings.HasSuffix(action, "key")
	title := "Choose PEM certificate / chain"
	field := "TLSCertFile"
	if private {
		title = "Choose unencrypted PEM private key"
		field = "TLSKeyFile"
	}
	title = c.tr(title)
	go func() {
		path, err := picker(title)
		var data []byte
		if err == nil && path != "" {
			data, err = readWebTLSFile(path, private)
		}
		c.layoutMu.Lock()
		c.server.mu.Lock()
		visible := c.server.webVisible && c.server.webConfigVisible && c.server.pairGeneration == generation
		c.server.busy = false
		c.server.message = "Server operation completed"
		if err != nil {
			c.server.message = err.Error()
		}
		if visible && err == nil && path != "" {
			if private {
				c.server.privateKeyPEM = data
			} else {
				c.server.certificatePEM = data
			}
			for i := range c.settings.fields {
				f := &c.settings.fields[i]
				if f.group == "Web" && f.name == field {
					f.editor.SetText(path)
				}
			}
			c.settings.focus = -1
		}
		c.server.mu.Unlock()
		c.layoutMu.Unlock()
		if c.invalidate != nil {
			c.invalidate()
		}
	}()
}
