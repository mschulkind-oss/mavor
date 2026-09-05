// Package wayland is a minimal Wayland client: just enough of the protocol to
// put a layer-shell surface on screen and paint pixels into it.
//
// It is deliberately not a general-purpose binding. There is no seat, no
// input, no xdg-shell and no EGL, because the overlay needs none of them — it
// is one surface that never takes focus and never reads a key. That narrowness
// is what makes hand-writing the protocol cheaper than depending on a C library
// which, being cgo, would forbid cross-compilation and static linking.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package wayland

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"
)

// Wayland's wire format is little-endian on every platform it supports.
var order = binary.LittleEndian

// ObjectID identifies a protocol object. IDs below 0xff000000 are allocated by
// the client; the server allocates from above that boundary.
type ObjectID uint32

// headerSize is the object ID plus the packed opcode/length word.
const headerSize = 8

// maxMessageSize is the largest a message may be: the length field is 16 bits.
const maxMessageSize = math.MaxUint16

// message is one decoded protocol message. Body holds the arguments still
// encoded; the caller knows the signature and decodes with a reader.
type message struct {
	Object ObjectID
	Opcode uint16
	Body   []byte
}

// builder encodes one outgoing request. Arguments are appended in signature
// order and finish() seals the header.
type builder struct {
	buf []byte
	fds []int
}

func newBuilder(obj ObjectID, opcode uint16) *builder {
	b := &builder{buf: make([]byte, headerSize, 64)}
	order.PutUint32(b.buf[0:4], uint32(obj))
	// The length half of the second word is filled in by finish().
	order.PutUint32(b.buf[4:8], uint32(opcode))
	return b
}

func (b *builder) putUint(v uint32)     { b.buf = order.AppendUint32(b.buf, v) }
func (b *builder) putInt(v int32)       { b.buf = order.AppendUint32(b.buf, uint32(v)) }
func (b *builder) putObject(v ObjectID) { b.putUint(uint32(v)) }

// putString writes a length-prefixed, NUL-terminated, 32-bit-padded string.
// A nil string is length 0; an empty string is length 1 (the NUL alone).
func (b *builder) putString(s string) {
	b.putUint(uint32(len(s) + 1))
	b.buf = append(b.buf, s...)
	b.buf = append(b.buf, 0)
	b.pad()
}

// putFD does not touch the message body at all: file descriptors travel out of
// band in the socket's ancillary data, and only their order has to match.
func (b *builder) putFD(fd int) { b.fds = append(b.fds, fd) }

func (b *builder) pad() {
	for len(b.buf)%4 != 0 {
		b.buf = append(b.buf, 0)
	}
}

// finish seals the length into the header and returns the wire bytes.
func (b *builder) finish() ([]byte, []int, error) {
	if len(b.buf) > maxMessageSize {
		return nil, nil, fmt.Errorf("wayland: message is %d bytes, over the %d-byte wire limit",
			len(b.buf), maxMessageSize)
	}
	// The second word packs length in the high 16 bits, opcode in the low.
	opcode := order.Uint32(b.buf[4:8]) & 0xffff
	order.PutUint32(b.buf[4:8], uint32(len(b.buf))<<16|opcode)
	return b.buf, b.fds, nil
}

var errShortMessage = errors.New("wayland: truncated message")

// reader decodes the arguments of one received event.
type reader struct {
	buf []byte
	pos int
	err error
}

func (r *reader) fail(err error) {
	if r.err == nil {
		r.err = err
	}
}

func (r *reader) take(n int) []byte {
	if r.err != nil {
		return nil
	}
	if r.pos+n > len(r.buf) {
		r.fail(errShortMessage)
		return nil
	}
	b := r.buf[r.pos : r.pos+n]
	r.pos += n
	return b
}

func (r *reader) uint() uint32 {
	b := r.take(4)
	if b == nil {
		return 0
	}
	return order.Uint32(b)
}

func (r *reader) object() ObjectID { return ObjectID(r.uint()) }

// str decodes a length-prefixed NUL-terminated string. The compositor is
// trusted for framing but not for content: a body claiming a length past the
// end of the message is a decode error, not a panic.
func (r *reader) str() string {
	n := int(r.uint())
	if r.err != nil {
		return ""
	}
	if n == 0 {
		return ""
	}
	padded := (n + 3) &^ 3
	b := r.take(padded)
	if b == nil {
		return ""
	}
	s := b[:n-1] // drop the trailing NUL
	if !utf8.Valid(s) {
		r.fail(errors.New("wayland: string argument is not valid UTF-8"))
		return ""
	}
	return string(s)
}

// parseMessages splits a received byte stream into whole messages, returning
// the number of bytes consumed. A partial trailing message is left for the
// next read.
func parseMessages(buf []byte) ([]message, int, error) {
	var msgs []message
	pos := 0
	for {
		if len(buf)-pos < headerSize {
			return msgs, pos, nil
		}
		obj := ObjectID(order.Uint32(buf[pos : pos+4]))
		word := order.Uint32(buf[pos+4 : pos+8])
		size := int(word >> 16)
		opcode := uint16(word & 0xffff)
		if size < headerSize {
			return nil, 0, fmt.Errorf("wayland: message claims %d bytes, below the %d-byte header", size, headerSize)
		}
		if len(buf)-pos < size {
			return msgs, pos, nil
		}
		body := make([]byte, size-headerSize)
		copy(body, buf[pos+headerSize:pos+size])
		msgs = append(msgs, message{Object: obj, Opcode: opcode, Body: body})
		pos += size
	}
}
