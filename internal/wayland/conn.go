package wayland

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// firstClientID is where client-allocated object IDs start. 1 is always
// wl_display, created implicitly by connecting.
const firstClientID ObjectID = 2

// displayID is the wl_display singleton, which exists before any exchange.
const displayID ObjectID = 1

// handler consumes one event for one object. Returning an error aborts the
// dispatch loop and surfaces on the connection.
type handler func(opcode uint16, r *reader) error

// Conn is a connection to a Wayland compositor. It owns the socket, allocates
// object IDs, and routes incoming events to per-object handlers.
//
// Requests may be sent from any goroutine. Events are dispatched on whichever
// goroutine calls Dispatch or Roundtrip, and handlers therefore run with the
// connection's lock released so they may send requests of their own.
type Conn struct {
	sock *net.UnixConn

	mu       sync.Mutex
	nextID   ObjectID
	handlers map[ObjectID]handler
	closed   bool

	readBuf  []byte
	oobBuf   []byte
	pending  []byte
	errEvent error // an error the compositor reported via wl_display.error
}

// Dial connects to the compositor named by $WAYLAND_DISPLAY, resolved against
// $XDG_RUNTIME_DIR unless it is already an absolute path.
func Dial() (*Conn, error) {
	display := os.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		display = "wayland-0"
	}
	path := display
	if !filepath.IsAbs(path) {
		dir := os.Getenv("XDG_RUNTIME_DIR")
		if dir == "" {
			return nil, errors.New("wayland: XDG_RUNTIME_DIR is unset, so $WAYLAND_DISPLAY cannot be resolved")
		}
		path = filepath.Join(dir, display)
	}

	sock, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("wayland: connect to %s: %w", path, err)
	}
	return &Conn{
		sock:     sock,
		nextID:   firstClientID,
		handlers: map[ObjectID]handler{},
		readBuf:  make([]byte, 8192),
		oobBuf:   make([]byte, syscall.CmsgSpace(4*8)),
	}, nil
}

// Close releases the socket. Idempotent.
func (c *Conn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	c.mu.Unlock()
	return c.sock.Close()
}

// newID allocates the next client object ID and registers its handler.
func (c *Conn) newID(h handler) ObjectID {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextID
	c.nextID++
	if h != nil {
		c.handlers[id] = h
	}
	return id
}

func (c *Conn) forget(id ObjectID) {
	c.mu.Lock()
	delete(c.handlers, id)
	c.mu.Unlock()
}

// send writes one request. File descriptors ride in the socket's ancillary
// data via SCM_RIGHTS, which is why this cannot be a plain Write.
func (c *Conn) send(b *builder) error {
	buf, fds, err := b.finish()
	if err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}

	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	if _, _, err := c.sock.WriteMsgUnix(buf, oob, nil); err != nil {
		return fmt.Errorf("wayland: send: %w", err)
	}
	return nil
}

// Dispatch reads at least once from the socket and runs the handler for every
// whole message received. It blocks until the socket has something to say.
func (c *Conn) Dispatch() error {
	// oobBuf is reused across calls the way readBuf already was. Dispatch
	// runs on every render tick for the life of the daemon, so a fresh
	// 48-byte buffer each time is permanent, pointless garbage.
	oob := c.oobBuf
	n, oobn, _, _, err := c.sock.ReadMsgUnix(c.readBuf, oob)
	if err != nil {
		return fmt.Errorf("wayland: receive: %w", err)
	}

	if oobn > 0 {
		scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return fmt.Errorf("wayland: parse ancillary data: %w", err)
		}
		// No event this client subscribes to carries a descriptor, so an
		// arriving one is unexpected. Close it rather than leaking the slot.
		for _, scm := range scms {
			fds, err := syscall.ParseUnixRights(&scm)
			if err != nil {
				continue
			}
			for _, fd := range fds {
				syscall.Close(fd)
			}
		}
	}

	c.pending = append(c.pending, c.readBuf[:n]...)
	msgs, consumed, err := parseMessages(c.pending)
	if err != nil {
		return err
	}
	// Slide the remainder to the front rather than advancing the slice
	// header. Reslicing forward strands the consumed bytes ahead of the
	// window, so the usable capacity shrinks on every read until append has
	// to reallocate; copying the tail back — almost always nothing, since a
	// partial message is the exception — keeps one buffer for the life of
	// the connection.
	c.pending = c.pending[:copy(c.pending, c.pending[consumed:])]

	for _, m := range msgs {
		c.mu.Lock()
		h := c.handlers[m.Object]
		c.mu.Unlock()
		if h == nil {
			// An event for an object we have already destroyed is normal:
			// the compositor may not have seen the destroy yet.
			continue
		}
		r := &reader{buf: m.Body}
		if err := h(m.Opcode, r); err != nil {
			return err
		}
		if r.err != nil {
			return fmt.Errorf("wayland: decoding event %d for object %d: %w", m.Opcode, m.Object, r.err)
		}
	}
	if c.errEvent != nil {
		return c.errEvent
	}
	return nil
}

// DispatchPending handles whatever has already arrived and returns at once.
//
// A Wayland client MUST read. Events it never reads pile up in the kernel
// buffer, and wl_buffer.release is one of them — without it a client cannot
// know when a buffer is free to reuse, and reusing a held buffer is a protocol
// violation the compositor answers by closing the connection with no error.
// pendingReadWindow is how long a drain waits for data already in flight. Long
// enough that the kernel reports what it has, short enough to be invisible
// against a 37 ms frame.
const pendingReadWindow = time.Millisecond

func (c *Conn) DispatchPending() error {
	for {
		// A deadline of exactly now is already past by the time the read
		// runs, and the read then reports a timeout WITHOUT looking at data
		// that is sitting there waiting. That silently drains nothing —
		// wl_buffer.release never arrives, every buffer stays marked busy,
		// and the overlay stops painting. A small positive window actually
		// reads what is queued.
		if err := c.sock.SetReadDeadline(time.Now().Add(pendingReadWindow)); err != nil {
			return err
		}
		err := c.Dispatch()
		_ = c.sock.SetReadDeadline(time.Time{})
		if err == nil {
			continue
		}
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return nil
		}
		return err
	}
}

// Roundtrip blocks until the compositor has processed every request sent so
// far, which is how a client knows the registry has finished advertising.
func (c *Conn) Roundtrip() error {
	done := false
	cb := c.newID(func(opcode uint16, r *reader) error {
		done = true
		return nil
	})
	defer c.forget(cb)

	b := newBuilder(displayID, 0) // wl_display.sync
	b.putObject(cb)
	if err := c.send(b); err != nil {
		return err
	}
	for !done {
		if err := c.Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

// watchDisplay registers the wl_display handler, which turns a protocol error
// from the compositor into a Go error rather than a silent hang.
func (c *Conn) watchDisplay() {
	c.mu.Lock()
	c.handlers[displayID] = func(opcode uint16, r *reader) error {
		switch opcode {
		case 0: // error(object_id, code, message)
			obj := r.object()
			code := r.uint()
			msg := r.str()
			c.errEvent = fmt.Errorf("wayland: compositor rejected object %d: %s (code %d)", obj, msg, code)
		case 1: // delete_id(id)
			c.forget(ObjectID(r.uint()))
		}
		return nil
	}
	c.mu.Unlock()
}
