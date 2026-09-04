package wayland

import (
	"bytes"
	"testing"
)

// The wire format is the one part of this package with an external
// specification, so it is worth pinning against hand-computed bytes rather
// than against itself.

func TestBuilderEncodesHeaderWithPackedLengthAndOpcode(t *testing.T) {
	b := newBuilder(ObjectID(2), 7)
	b.putUint(0xdeadbeef)

	buf, fds, err := b.finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(fds) != 0 {
		t.Errorf("fds = %v, want none", fds)
	}
	want := []byte{
		0x02, 0x00, 0x00, 0x00, // object id 2
		0x07, 0x00, 0x0c, 0x00, // opcode 7, size 12
		0xef, 0xbe, 0xad, 0xde, // argument
	}
	if !bytes.Equal(buf, want) {
		t.Errorf("encoded % x, want % x", buf, want)
	}
}

// A string is length-prefixed *including* its NUL, then padded to 4 bytes.
// Getting either half wrong desynchronises every later argument, so both are
// pinned here.
func TestBuilderStringIsNulTerminatedAndPadded(t *testing.T) {
	cases := []struct {
		in   string
		want []byte
	}{
		{"", []byte{0x01, 0, 0, 0, 0x00, 0, 0, 0}},
		{"a", []byte{0x02, 0, 0, 0, 'a', 0x00, 0, 0}},
		{"abc", []byte{0x04, 0, 0, 0, 'a', 'b', 'c', 0x00}},
		{"abcd", []byte{0x05, 0, 0, 0, 'a', 'b', 'c', 'd', 0x00, 0, 0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			b := newBuilder(1, 0)
			b.putString(tc.in)
			buf, _, err := b.finish()
			if err != nil {
				t.Fatal(err)
			}
			if got := buf[headerSize:]; !bytes.Equal(got, tc.want) {
				t.Errorf("putString(%q) = % x, want % x", tc.in, got, tc.want)
			}
		})
	}
}

func TestStringRoundTrips(t *testing.T) {
	for _, s := range []string{"", "a", "wl_compositor", "zwlr_layer_shell_v1", "ünïcödé"} {
		b := newBuilder(1, 0)
		b.putString(s)
		buf, _, err := b.finish()
		if err != nil {
			t.Fatal(err)
		}
		r := &reader{buf: buf[headerSize:]}
		if got := r.str(); got != s {
			t.Errorf("round trip of %q = %q", s, got)
		}
		if r.err != nil {
			t.Errorf("round trip of %q errored: %v", s, r.err)
		}
	}
}

// File descriptors travel in the socket's ancillary data, never in the body.
// A message carrying one must be the same length as a message without.
func TestFDsAreNotWrittenIntoTheBody(t *testing.T) {
	b := newBuilder(1, 0)
	b.putUint(5)
	b.putFD(42)
	b.putUint(6)

	buf, fds, err := b.finish()
	if err != nil {
		t.Fatal(err)
	}
	if want := headerSize + 8; len(buf) != want {
		t.Errorf("body is %d bytes, want %d — the fd leaked into the body", len(buf), want)
	}
	if len(fds) != 1 || fds[0] != 42 {
		t.Errorf("fds = %v, want [42]", fds)
	}
}

// The socket delivers a byte stream, not framed messages, so a read can end
// mid-message. parseMessages must consume only whole ones.
func TestParseMessagesLeavesPartialTrailingMessage(t *testing.T) {
	b1 := newBuilder(1, 0)
	b1.putUint(11)
	m1, _, _ := b1.finish()

	b2 := newBuilder(2, 1)
	b2.putUint(22)
	m2, _, _ := b2.finish()

	stream := append(append([]byte{}, m1...), m2...)

	for cut := len(m1); cut < len(stream); cut++ {
		msgs, n, err := parseMessages(stream[:cut])
		if err != nil {
			t.Fatalf("cut at %d: %v", cut, err)
		}
		if len(msgs) != 1 || n != len(m1) {
			t.Fatalf("cut at %d: got %d messages consuming %d bytes, want 1 consuming %d",
				cut, len(msgs), n, len(m1))
		}
	}

	msgs, n, err := parseMessages(stream)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 || n != len(stream) {
		t.Fatalf("whole stream: %d messages consuming %d bytes, want 2 consuming %d", len(msgs), n, len(stream))
	}
	if msgs[1].Object != 2 || msgs[1].Opcode != 1 {
		t.Errorf("second message = object %d opcode %d, want 2/1", msgs[1].Object, msgs[1].Opcode)
	}
}

// A malformed size field must be an error, not a panic or an infinite loop.
func TestParseMessagesRejectsUndersizedHeader(t *testing.T) {
	bad := make([]byte, headerSize)
	order.PutUint32(bad[0:4], 1)
	order.PutUint32(bad[4:8], uint32(4)<<16) // claims 4 bytes, less than the header
	if _, _, err := parseMessages(bad); err == nil {
		t.Error("parseMessages accepted a message claiming fewer bytes than its header")
	}
}

// A truncated body must surface as a decode error rather than reading past the
// end of the buffer.
func TestReaderReportsTruncation(t *testing.T) {
	r := &reader{buf: []byte{0x01, 0x00}} // two bytes where a uint32 is expected
	r.uint()
	if r.err == nil {
		t.Error("reader accepted a truncated uint32")
	}

	r = &reader{buf: []byte{0xff, 0xff, 0xff, 0xff}} // string claiming 4GB
	r.str()
	if r.err == nil {
		t.Error("reader accepted a string longer than its message")
	}
}
