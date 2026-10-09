package goconfig

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// The target server owns the HTTP listener and optional HTTPS certificate paths.
// PublicURL is read only for migration from the previous single-address format.
type WebConfig struct {
	Enabled       bool   `json:"enabled"`
	ListenAddress string `json:"listen_address"`
	ListenPort    int    `json:"listen_port"`
	TLS           bool   `json:"tls"`
	TLSCertFile   string `json:"tls_cert_file"`
	TLSKeyFile    string `json:"tls_key_file"`
	PublicURL     string `json:"public_url,omitempty"`
}

func (c *WebConfig) UnmarshalJSON(data []byte) error {
	type plain WebConfig
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*c = WebConfig(value)
	return nil
}

func (c WebConfig) Normalized() WebConfig {
	c.ListenAddress = strings.TrimSpace(c.ListenAddress)
	c.TLSCertFile = strings.TrimSpace(c.TLSCertFile)
	c.TLSKeyFile = strings.TrimSpace(c.TLSKeyFile)
	legacy := strings.TrimRight(strings.TrimSpace(c.PublicURL), "/")
	if legacy != "" && c.ListenPort == 0 {
		u, err := url.Parse(legacy)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Path != "" || strings.HasSuffix(u.Host, ":") {
			c.PublicURL = legacy
			return c
		}
		port := u.Port()
		if port == "" {
			port = "80"
			if u.Scheme == "https" {
				port = "443"
			}
		}
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			c.PublicURL = legacy
			return c
		}
		c.ListenAddress = u.Hostname()
		c.ListenPort = n
		c.TLS = u.Scheme == "https"
	}
	c.PublicURL = ""
	if c.ListenAddress == "" && c.ListenPort == 0 {
		c.ListenAddress = "127.0.0.1"
		c.ListenPort = 8080
	}
	c.ListenAddress = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(c.ListenAddress, "["), "]"))
	if ip := net.ParseIP(c.ListenAddress); ip != nil {
		c.ListenAddress = ip.String()
	}
	return c
}

func (c WebConfig) Validate() error {
	c = c.Normalized()
	if c.PublicURL != "" {
		return fmt.Errorf("legacy Web address must be a valid HTTP or HTTPS origin")
	}
	if c.ListenAddress == "" || strings.ContainsAny(c.ListenAddress, "/@?#\\ \t\r\n") {
		return fmt.Errorf("HTTP listen address must be an IP address or hostname, without scheme or port")
	}
	if ip := net.ParseIP(c.ListenAddress); ip != nil {
		if ip.IsMulticast() {
			return fmt.Errorf("HTTP listen address cannot be multicast")
		}
	} else {
		for _, r := range c.ListenAddress {
			if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '-') {
				return fmt.Errorf("HTTP listen address must be an IP address or hostname, without scheme or port")
			}
		}
	}
	if c.ListenPort < 1 || c.ListenPort > 65535 {
		return fmt.Errorf("HTTP listen port must be between 1 and 65535")
	}
	if c.TLS && (c.TLSCertFile == "" || c.TLSKeyFile == "") {
		return fmt.Errorf("Web TLS certificate and key files are required")
	}
	return nil
}

func (c WebConfig) BindAddress() (string, error) {
	c = c.Normalized()
	if err := c.Validate(); err != nil {
		return "", err
	}
	return net.JoinHostPort(c.ListenAddress, strconv.Itoa(c.ListenPort)), nil
}
func (c WebConfig) UsesTLS() bool { return c.Normalized().TLS }

// Origin is an access address, never the editable listener configuration.
// A wildcard listener's concrete access origins are chosen on the target server.
func (c WebConfig) Origin() string {
	c = c.Normalized()
	scheme := "http"
	defaultPort := 80
	if c.TLS {
		scheme = "https"
		defaultPort = 443
	}
	host := c.ListenAddress
	if c.ListenPort != defaultPort {
		host = net.JoinHostPort(host, strconv.Itoa(c.ListenPort))
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host
}
