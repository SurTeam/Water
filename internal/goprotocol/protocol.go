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
	if err:=writeAll(w,lenbuf[:]);err!=nil{return err}
	return writeAll(w,payload)
}

func WriteTerminal(w io.Writer, id uuid.UUID, ev TerminalEvent) error {
	// WT4 terminal frames are hot-path binary data. Build the fixed header on
	// the stack and write output bytes directly instead of copying every event
	// through bytes.Buffer.
	var frame [38]byte
	copy(frame[4:8], terminalPrefix[:])

	payloadLen:=0
	switch ev.Kind {
	case OutputEvent:
		frame[8]=terminalOutput
		payloadLen=33+len(ev.Data)
	case ResizeEvent:
		frame[8]=terminalResize
		payloadLen=33
	case ExitEvent:
		frame[8]=terminalExit
		payloadLen=34
	default:
		return errors.New("unknown terminal event kind")
	}
	if payloadLen>MaxFrameBytes {
		return fmt.Errorf("frame too large: %d",payloadLen)
	}
	binary.BigEndian.PutUint32(frame[0:4],uint32(payloadLen))
	copy(frame[9:25],id[:])
	binary.BigEndian.PutUint64(frame[25:33],ev.Seq)

	switch ev.Kind {
	case OutputEvent,ResizeEvent:
		size:=ev.Size.Normalized()
		binary.BigEndian.PutUint16(frame[33:35],uint16(size.Columns))
		binary.BigEndian.PutUint16(frame[35:37],uint16(size.Lines))
		if err:=writeAll(w,frame[:37]);err!=nil{return err}
		if ev.Kind==OutputEvent {
			return writeAll(w,ev.Data)
		}
		return nil
	case ExitEvent:
		if ev.Code==nil {
			frame[33]=0
			for i:=34;i<38;i++{frame[i]=0}
		}else{
			frame[33]=1
			binary.BigEndian.PutUint32(frame[34:38],uint32(*ev.Code))
		}
		return writeAll(w,frame[:38])
	default:
		panic("unreachable")
	}
}

func writeAll(w io.Writer,data []byte)error{
	for len(data)>0{
		n,err:=w.Write(data)
		if err!=nil{return err}
		if n<=0{return io.ErrShortWrite}
		data=data[n:]
	}
	return nil
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
			ev.Data = rest[4:]
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
