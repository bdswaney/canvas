// Package lib0 frames the lib0 encoding used by the Yjs sync and awareness
// protocols. Numbers are unsigned LEB128: seven payload bits per byte, low
// byte first, with 0x80 set on every byte except the last.
package lib0

import (
	"errors"
	"fmt"
)

var errTruncated = errors.New("truncated message")

const maxVarUintBytes = 10

// AppendVarUint appends value as a varuint.
func AppendVarUint(dst []byte, value uint64) []byte {
	for value > 0x7f {
		dst = append(dst, byte(value)|0x80)
		value >>= 7
	}
	return append(dst, byte(value))
}

// AppendVarBytes appends payload prefixed by its varuint length.
func AppendVarBytes(dst, payload []byte) []byte {
	return append(AppendVarUint(dst, uint64(len(payload))), payload...)
}

// Reader walks a message without copying; returned slices alias the input.
type Reader struct {
	buf []byte
	pos int
}

// NewReader returns a Reader positioned at the start of buf.
func NewReader(buf []byte) *Reader { return &Reader{buf: buf} }

func (r *Reader) done() bool { return r.pos >= len(r.buf) }

// VarUint reads the next varuint.
func (r *Reader) VarUint() (uint64, error) {
	var value uint64
	var shift uint
	for n := 0; n < maxVarUintBytes; n++ {
		if r.done() {
			return 0, errTruncated
		}
		b := r.buf[r.pos]
		r.pos++
		value |= uint64(b&0x7f) << shift
		if b&0x80 == 0 {
			return value, nil
		}
		shift += 7
	}
	return 0, fmt.Errorf("varuint longer than %d bytes", maxVarUintBytes)
}

// VarBytes reads the next length-prefixed byte string.
func (r *Reader) VarBytes() ([]byte, error) {
	length, err := r.VarUint()
	if err != nil {
		return nil, err
	}
	if length > uint64(len(r.buf)-r.pos) {
		return nil, errTruncated
	}
	start := r.pos
	r.pos += int(length)
	return r.buf[start:r.pos], nil
}
