package goprotocol

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

const (
	ProtocolVersion = 4
	APISignature    = "water-control/v5"
	MaxFrameBytes   = 16 * 1024 * 1024

	terminalOutput = byte(1)
	terminalResize = byte(2)
	terminalExit   = byte(3)
)

var terminalPrefix = [4]byte{0, 'W', 'T', '4'}

type WireMessage struct {
	BuildVariant    string          `json:"build_variant,omitempty"`
	ProtocolVersion uint32          `json:"protocol_version"`
	RequestID       uint64          `json:"request_id"`
	Method          string          `json:"method,omitempty"`
	Params          json.RawMessage `json:"params,omitempty"`
	OK              *bool           `json:"ok,omitempty"`
	Result          json.RawMessage `json:"result,omitempty"`
	Error           *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type TerminalSize struct {
	Columns int `json:"columns"`
	Lines   int `json:"lines"`
}

func (s TerminalSize) Normalized() TerminalSize {
	if s.Columns < 2 {
		s.Columns = 2
	}
	if s.Columns > 512 {
		s.Columns = 512
	}
	if s.Lines < 1 {
		s.Lines = 1
	}
	if s.Lines > 256 {
		s.Lines = 256
	}
	return s
}

type TerminalEventKind uint8

const (
	OutputEvent TerminalEventKind = iota + 1
	ResizeEvent
	ExitEvent
)

type TerminalEvent struct {
	Kind TerminalEventKind
	Seq  uint64
	Size TerminalSize
	Data []byte
	Code *int32
}

type WireTerminalEvent struct {
	Type    string `json:"type"`
	Seq     uint64 `json:"seq"`
	Bytes   string `json:"bytes,omitempty"`
	Columns int    `json:"columns,omitempty"`
	Lines   int    `json:"lines,omitempty"`
	Code    *int32 `json:"code,omitempty"`
}

func (e TerminalEvent) Wire() WireTerminalEvent {
	switch e.Kind {
	case OutputEvent:
		return WireTerminalEvent{Type: "output", Seq: e.Seq, Bytes: base64.StdEncoding.EncodeToString(e.Data)}
	case ResizeEvent:
		return WireTerminalEvent{Type: "resize", Seq: e.Seq, Columns: e.Size.Columns, Lines: e.Size.Lines}
	default:
		return WireTerminalEvent{Type: "exit", Seq: e.Seq, Code: e.Code}
	}
}

type Frame struct {
	JSON       []byte
	TerminalID uuid.UUID
	Terminal   *TerminalEvent
}

func WriteJSON(w io.Writer, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writePayload(w, payload)
}

func writePayload(w io.Writer, payload []byte) error {
	if len(payload) > MaxFrameBytes {
		return fmt.Errorf("frame too large: %d", len(payload))
	}
	var lenbuf [4]byte
	binary.BigEndian.PutUint32(lenbuf[:], uint32(len(payload)))
	if _, err := w.Write(lenbuf[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func WriteTerminal(w io.Writer, id uuid.UUID, ev TerminalEvent) error {
	var payload bytes.Buffer
	payload.Write(terminalPrefix[:])
	switch ev.Kind {
	case OutputEvent:
		payload.WriteByte(terminalOutput)
	case ResizeEvent:
		payload.WriteByte(terminalResize)
	case ExitEvent:
		payload.WriteByte(terminalExit)
	default:
		return errors.New("unknown terminal event kind")
	}
	payload.Write(id[:])
	var seq [8]byte
	binary.BigEndian.PutUint64(seq[:], ev.Seq)
	payload.Write(seq[:])

	switch ev.Kind {
	case OutputEvent, ResizeEvent:
		size := ev.Size.Normalized()
		var geometry [4]byte
		binary.BigEndian.PutUint16(geometry[0:2], uint16(size.Columns))
		binary.BigEndian.PutUint16(geometry[2:4], uint16(size.Lines))
		payload.Write(geometry[:])
		if ev.Kind == OutputEvent {
			payload.Write(ev.Data)
		}
	case ExitEvent:
		if ev.Code == nil {
			payload.WriteByte(0)
			payload.Write([]byte{0, 0, 0, 0})
		} else {
			payload.WriteByte(1)
			var code [4]byte
			binary.BigEndian.PutUint32(code[:], uint32(*ev.Code))
			payload.Write(code[:])
		}
	}
	return writePayload(w, payload.Bytes())
}

func ReadFrame(r io.Reader) (Frame, error) {
	var lenbuf [4]byte
	if _, err := io.ReadFull(r, lenbuf[:]); err != nil {
		return Frame{}, err
	}
	n := int(binary.BigEndian.Uint32(lenbuf[:]))
	if n < 0 || n > MaxFrameBytes {
		return Frame{}, fmt.Errorf("invalid frame length %d", n)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return Frame{}, err
	}
	if n < 4 || !bytes.Equal(payload[:4], terminalPrefix[:]) {
		return Frame{JSON: payload}, nil
	}
	if n < 29 {
		return Frame{}, errors.New("truncated terminal frame")
	}
	kind := payload[4]
	var id uuid.UUID
	copy(id[:], payload[5:21])
	seq := binary.BigEndian.Uint64(payload[21:29])
	rest := payload[29:]
	ev := TerminalEvent{Seq: seq}

	switch kind {
	case terminalOutput, terminalResize:
		if len(rest) < 4 {
			return Frame{}, errors.New("truncated terminal geometry")
		}
		ev.Size = TerminalSize{
			Columns: int(binary.BigEndian.Uint16(rest[:2])),
			Lines:   int(binary.BigEndian.Uint16(rest[2:4])),
		}.Normalized()
		if kind == terminalOutput {
			ev.Kind = OutputEvent
			ev.Data = append([]byte(nil), rest[4:]...)
		} else {
			if len(rest) != 4 {
				return Frame{}, errors.New("resize frame has trailing bytes")
			}
			ev.Kind = ResizeEvent
		}
	case terminalExit:
		if len(rest) != 5 {
			return Frame{}, errors.New("invalid exit frame")
		}
		ev.Kind = ExitEvent
		if rest[0] != 0 {
			v := int32(binary.BigEndian.Uint32(rest[1:5]))
			ev.Code = &v
		}
	default:
		return Frame{}, fmt.Errorf("unknown terminal frame type %d", kind)
	}
	return Frame{TerminalID: id, Terminal: &ev}, nil
}

func Success(requestID uint64, value any) WireMessage {
	ok := true
	result, _ := json.Marshal(value)
	return WireMessage{ProtocolVersion: ProtocolVersion, RequestID: requestID, OK: &ok, Result: result}
}

func Failure(requestID uint64, code, message string) WireMessage {
	ok := false
	return WireMessage{
		ProtocolVersion: ProtocolVersion,
		RequestID:       requestID,
		OK:              &ok,
		Error:           &RPCError{Code: code, Message: message},
	}
}
