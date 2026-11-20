package hc

import (
	"bufio"
	"bytes"
	"testing"
)

func TestFrameRoundtrip(t *testing.T) {
	frames := []frame{
		{ch: 0, kind: frameData, payload: []byte(`{"jsonrpc":"2.0","id":1,"method":"hello"}`)},
		{ch: 7, kind: frameData, payload: []byte("first paint \x1b[1mbold\x1b[0m")},
		{ch: 7, kind: frameCredit, payload: []byte{0x00, 0x10, 0x00, 0x00}},
		{ch: 7, kind: frameClose, payload: []byte(`{"reason":"exited","exit_code":0}`)},
		{ch: 7, kind: frameResize, payload: []byte{0x50, 0x00, 0x18, 0x00}},
		{ch: 7, kind: frameFocus, payload: []byte{1}},
		{ch: 7, kind: frameAutoInput, payload: []byte("\x1b[?1;2c")},
		{ch: 7, kind: frameData}, // empty payload: the detach CLOSE ack shape
	}

	var buf bytes.Buffer
	for _, f := range frames {
		if err := writeFrame(&buf, f); err != nil {
			t.Fatalf("writeFrame %+v: %v", f, err)
		}
	}
	reader := bufio.NewReader(&buf)
	for i, want := range frames {
		got, err := readFrame(reader)
		if err != nil {
			t.Fatalf("readFrame %d: %v", i, err)
		}
		if got.ch != want.ch || got.kind != want.kind || !bytes.Equal(got.payload, want.payload) {
			t.Fatalf("frame %d = %+v, want %+v", i, got, want)
		}
	}
	if _, err := readFrame(reader); err == nil {
		t.Fatal("readFrame past end: expected EOF")
	}
}

func TestFrameTooLargeRejected(t *testing.T) {
	// Write path.
	big := frame{ch: 7, kind: frameData, payload: make([]byte, maxPayload+1)}
	if err := writeFrame(&bytes.Buffer{}, big); err != errFrameTooLarge {
		t.Fatalf("write oversized: %v, want errFrameTooLarge", err)
	}

	// Read path: a peer header claiming over the cap is refused before any
	// payload allocation.
	exactly := frame{ch: 7, kind: frameData, payload: make([]byte, maxPayload)}
	var buf bytes.Buffer
	if err := writeFrame(&buf, exactly); err != nil {
		t.Fatalf("write at-cap frame: %v", err)
	}
	hdr := buf.Bytes()[:9]
	hdr[5] = 0x80 // len := 0x80000000, far above the cap
	if _, err := readFrame(bufio.NewReader(&buf)); err != errFrameTooLarge {
		t.Fatalf("read oversized: %v, want errFrameTooLarge", err)
	}
}
