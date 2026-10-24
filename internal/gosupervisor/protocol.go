// Package gosupervisor owns PTYs for Water servers. The supervisor outlives the
// server process so an upgraded server can re-attach to terminals whose programs
// are still running, instead of restarting them.
package gosupervisor

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Frame layout: [1 byte kind][4 byte big-endian payload length][payload].
// Control payloads are JSON; output payloads carry raw terminal bytes so the
// hot path never encodes text.
const (
	kindSpawn      byte = 1
	kindAttach     byte = 2
	kindWrite      byte = 3
	kindResize     byte = 4
	kindClose      byte = 5
	kindList       byte = 6
	kindShutdown   byte = 7
	kindForeground byte = 8

	kindSpawned      byte = 101
	kindOutput       byte = 102
	kindResized      byte = 103
	kindExited       byte = 104
	kindListed       byte = 105
	kindFailure      byte = 106
	kindAttached     byte = 107
	kindForegrounded byte = 108
)

// maxFrameBytes bounds a single frame so a corrupt length cannot exhaust memory.
const maxFrameBytes = 8 * 1024 * 1024

type spawnRequest struct {
	ID      string   `json:"id"`
	Program string   `json:"program"`
	Args    []string `json:"args"`
	Cwd     string   `json:"cwd"`
	Env     []string `json:"env"`
	Lines   uint16   `json:"lines"`
	Columns uint16   `json:"columns"`
	CellW   uint16   `json:"cell_w"`
	CellH   uint16   `json:"cell_h"`
	// Replace closes a live terminal with the same id first. Resume, used by
	// recovery, keeps it instead.
	Replace bool `json:"replace,omitempty"`
}

type terminalRef struct {
	ID string `json:"id"`
}

type attachRequest struct {
	ID     string `json:"id"`
	Offset uint64 `json:"offset"`
}

type writeRequest struct {
	ID   string `json:"id"`
	Data []byte `json:"data"`
}

type resizeRequest struct {
	ID      string `json:"id"`
	Lines   uint16 `json:"lines"`
	Columns uint16 `json:"columns"`
	CellW   uint16 `json:"cell_w"`
	CellH   uint16 `json:"cell_h"`
}

type spawnedResponse struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
	PID        int    `json:"pid"`
	Resumed    bool   `json:"resumed"`
	Error      string `json:"error,omitempty"`
}

// attachedResponse reports how much history the supervisor still holds for the
// terminal. A resumed terminal keeps running across server restarts.
type attachedResponse struct {
	ID         string `json:"id"`
	PID        int    `json:"pid"`
	Offset     uint64 `json:"offset"`
	Available  uint64 `json:"available"`
	Truncated  bool   `json:"truncated"`
	Restore    []byte `json:"restore,omitempty"`
	Generation uint64 `json:"generation"`
	Exited     bool   `json:"exited"`
	Code       int32  `json:"code"`
	Error      string `json:"error,omitempty"`
}

type resizedEvent struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
	Lines      uint16 `json:"lines"`
	Columns    uint16 `json:"columns"`
}

type exitedEvent struct {
	ID         string `json:"id"`
	Generation uint64 `json:"generation"`
	Code       int32  `json:"code"`
}

type listedResponse struct {
	Terminals []listedTerminal `json:"terminals"`
}

type listedTerminal struct {
	ID     string `json:"id"`
	PID    int    `json:"pid"`
	Exited bool   `json:"exited"`
}

type foregroundResponse struct {
	ID    string `json:"id"`
	Group int    `json:"group"`
	Error string `json:"error,omitempty"`
}

type failureResponse struct {
	Error string `json:"error"`
}

// outputPayload is the header of an output frame: the terminal id followed by the
// raw bytes. Keeping it binary avoids copying the payload through JSON.
type outputHeader struct {
	id         [16]byte
	offset     uint64
	generation uint64
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxFrameBytes {
		return fmt.Errorf("supervisor frame too large: %d", len(payload))
	}
	var header [5]byte
	header[0] = kind
	binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var header [5]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, err
	}
	length := binary.BigEndian.Uint32(header[1:])
	if length > maxFrameBytes {
		return 0, nil, fmt.Errorf("supervisor frame too large: %d", length)
	}
	if length == 0 {
		return header[0], nil, nil
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func unmarshal(payload []byte, value any) error { return json.Unmarshal(payload, value) }

func writeJSON(w io.Writer, kind byte, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeFrame(w, kind, payload)
}

const outputHeaderBytes = 32

func writeOutput(w io.Writer, id [16]byte, generation, offset uint64, data []byte) error {
	payload := make([]byte, outputHeaderBytes+len(data))
	copy(payload, id[:])
	binary.BigEndian.PutUint64(payload[16:], generation)
	binary.BigEndian.PutUint64(payload[24:], offset)
	copy(payload[outputHeaderBytes:], data)
	return writeFrame(w, kindOutput, payload)
}

func parseOutput(payload []byte) (outputHeader, []byte, error) {
	if len(payload) < outputHeaderBytes {
		return outputHeader{}, nil, errors.New("short output frame")
	}
	var header outputHeader
	copy(header.id[:], payload[:16])
	header.generation = binary.BigEndian.Uint64(payload[16:])
	header.offset = binary.BigEndian.Uint64(payload[24:])
	return header, payload[outputHeaderBytes:], nil
}
