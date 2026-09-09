package main

import (
	"bytes"
	"testing"
)

func TestVarUintRoundTrip(t *testing.T) {
	// Boundaries where the encoded length grows by one byte.
	for _, value := range []uint64{0, 1, 127, 128, 129, 16383, 16384, 2097151, 2097152, 1 << 40} {
		encoded := appendVarUint(nil, value)
		if encoded[len(encoded)-1]&0x80 != 0 {
			// Continuation bit must be clear on the final byte only.
			for _, b := range encoded[:len(encoded)-1] {
				if b&0x80 == 0 {
					t.Fatalf("%d: early terminator in % x", value, encoded)
				}
			}
		}
		r := &reader{buf: encoded}
		got, err := r.varUint()
		if err != nil {
			t.Fatalf("%d: %v", value, err)
		}
		if got != value || !r.done() {
			t.Fatalf("%d: decoded %d, %d bytes left", value, got, len(encoded)-r.pos)
		}
	}
}

func TestVarUintEncoding(t *testing.T) {
	// Checked against lib0's encoding.writeVarUint.
	for _, tt := range []struct {
		value uint64
		want  []byte
	}{
		{0, []byte{0x00}},
		{1, []byte{0x01}},
		{127, []byte{0x7f}},
		{128, []byte{0x80, 0x01}},
		{16384, []byte{0x80, 0x80, 0x01}},
	} {
		if got := appendVarUint(nil, tt.value); !bytes.Equal(got, tt.want) {
			t.Errorf("%d encoded as % x, want % x", tt.value, got, tt.want)
		}
	}
}

func TestVarBytesRoundTrip(t *testing.T) {
	payload := bytes.Repeat([]byte{0xab}, 300)
	r := &reader{buf: appendVarBytes([]byte{0x09}, payload)}
	if tag, err := r.varUint(); err != nil || tag != 9 {
		t.Fatalf("tag = %d, %v", tag, err)
	}
	got, err := r.varBytes()
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: %v", err)
	}
	if !r.done() {
		t.Error("trailing bytes")
	}
}

func TestTruncatedMessages(t *testing.T) {
	for _, buf := range [][]byte{{}, {0x80}, {0x00, 0x05, 0x01}} {
		r := &reader{buf: buf}
		if _, err := r.varUint(); err != nil {
			continue
		}
		if _, err := r.varBytes(); err == nil {
			t.Errorf("% x: expected error", buf)
		}
	}
}
