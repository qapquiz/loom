package hc

// Unexported wire layer: frame codec, JSON-RPC envelopes, hello negotiation.
// Nothing here is exported; callers see only the types in hc.go, stream.go and
// event.go. Frame shape per hc 0.1.21 schema: [channel u32 LE][kind u8]
// [len u32 LE][payload], payload at most 16 MiB.

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
)

const maxPayload = 16 << 20

type frameKind uint8

const (
	frameData frameKind = iota
	frameCredit
	frameClose
	frameResize
	frameFocus
	frameFacts
	frameAutoInput
)

type frame struct {
	ch      uint32
	kind    frameKind
	payload []byte
}

var errFrameTooLarge = errors.New("hc: frame payload exceeds the 16 MiB cap")

func appendFrame(dst []byte, f frame) []byte {
	var hdr [9]byte
	binary.LittleEndian.PutUint32(hdr[0:4], f.ch)
	hdr[4] = byte(f.kind)
	binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(f.payload)))
	dst = append(dst, hdr[:]...)
	return append(dst, f.payload...)
}

func writeFrame(w io.Writer, f frame) error {
	if len(f.payload) > maxPayload {
		return errFrameTooLarge
	}
	// One Write per frame: header and payload never interleave across writers.
	buf := appendFrame(make([]byte, 0, 9+len(f.payload)), f)
	_, err := w.Write(buf)
	return err
}

func readFrame(br *bufio.Reader) (frame, error) {
	var hdr [9]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return frame{}, err
	}
	f := frame{
		ch:   binary.LittleEndian.Uint32(hdr[0:4]),
		kind: frameKind(hdr[4]),
	}
	n := binary.LittleEndian.Uint32(hdr[5:9])
	if uint64(n) > maxPayload {
		return frame{}, errFrameTooLarge
	}
	if n > 0 {
		f.payload = make([]byte, n)
		if _, err := io.ReadFull(br, f.payload); err != nil {
			return frame{}, err
		}
	}
	return f, nil
}

// JSON-RPC 2.0 envelopes, one message per line (plain) or per channel-0 DATA
// frame (framed).

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      uint64 `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    *struct {
		Kind string `json:"kind"`
	} `json:"data"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return "hc: ok"
	}
	if e.Data != nil && e.Data.Kind != "" {
		return "hc: " + e.Message + " (" + e.Data.Kind + ")"
	}
	return "hc: " + e.Message
}

// rpcMessage parses both sides of channel 0: replies carry an id, core
// notifications (event/heartbeat/missed in plain-mode shape) carry a method.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *uint64         `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type helloParams struct {
	Framed     bool     `json:"framed"`
	Features   []string `json:"features,omitempty"`
	Generation uint32   `json:"generation,omitempty"`
}

type helloResult struct {
	Framed     bool     `json:"framed"`
	Features   []string `json:"features"`
	Generation uint32   `json:"generation"`
	Version    string   `json:"version"`
}

func hasFeature(features []string, name string) bool {
	for _, f := range features {
		if f == name {
			return true
		}
	}
	return false
}
