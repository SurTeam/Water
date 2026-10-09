package goprotocol

import "time"

const WebCapability = "web-access/v1"
const WebTLSImportCapability = "web-tls-import/v1"

type WebStatus struct {
	State         string `json:"state"`
	ListenAddress string `json:"listen_address"`
	PublicURL     string `json:"public_url"`
	Error         string `json:"error,omitempty"`
}

type WebPairing struct {
	ID        string    `json:"id"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type WebDevice struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}
