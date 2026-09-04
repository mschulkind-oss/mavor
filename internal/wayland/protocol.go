package wayland

import (
	"fmt"
	"os"
)

// Opcodes and signatures below were taken from wayland.xml and
// wlr-layer-shell-unstable-v1.xml. An opcode is an interface's request or
// event index in declaration order, so it is positional and silent when wrong:
// a mistake here desynchronises the stream rather than failing loudly. The
// wire signature is repeated in a comment above each call for that reason.

// Layer is which wlr-layer-shell layer a surface sits on. The overlay uses Top,
// which is above normal windows but below fullscreen and lock screens.
type Layer uint32

const (
	LayerBackground Layer = 0
	LayerBottom     Layer = 1
	LayerTop        Layer = 2
	LayerOverlay    Layer = 3
)

// Anchor edges, combined as a bitmask. Anchoring to opposite edges stretches
// the surface between them.
const (
	AnchorTop    uint32 = 1
	AnchorBottom uint32 = 2
	AnchorLeft   uint32 = 4
	AnchorRight  uint32 = 8
)

// formatARGB8888 is wl_shm.format's code for 32-bit ARGB, premultiplied. Every
// compositor is required to support it, so the client never negotiates.
const formatARGB8888 uint32 = 0

// Display is a connected compositor with the globals this client needs.
type Display struct {
	conn *Conn

	compositor ObjectID
	shm        ObjectID
	layerShell ObjectID
}

// Connect dials the compositor and binds the three globals the overlay needs.
// It fails if the compositor does not implement wlr-layer-shell, which is the
// honest outcome: without it there is nowhere to put an overlay.
func Connect() (*Display, error) {
	conn, err := Dial()
	if err != nil {
		return nil, err
	}
	conn.watchDisplay()

	d := &Display{conn: conn}
	type global struct {
		name    uint32
		version uint32
	}
	found := map[string]global{}

	registry := conn.newID(func(opcode uint16, r *reader) error {
		if opcode != 0 { // global(name, interface, version)
			return nil
		}
		name := r.uint()
		iface := r.str()
		version := r.uint()
		found[iface] = global{name: name, version: version}
		return nil
	})

	// wl_display.get_registry(registry:new_id)
	b := newBuilder(displayID, 1)
	b.putObject(registry)
	if err := conn.send(b); err != nil {
		conn.Close()
		return nil, err
	}
	if err := conn.Roundtrip(); err != nil {
		conn.Close()
		return nil, err
	}

	bind := func(iface string, want uint32) (ObjectID, error) {
		g, ok := found[iface]
		if !ok {
			return 0, fmt.Errorf("wayland: compositor does not offer %s", iface)
		}
		version := min(want, g.version)
		id := conn.newID(nil)
		// wl_registry.bind(name:uint, id:new_id) — the new_id is untyped here,
		// so the interface name and version precede it on the wire.
		b := newBuilder(registry, 0)
		b.putUint(g.name)
		b.putString(iface)
		b.putUint(version)
		b.putObject(id)
		return id, conn.send(b)
	}

	if d.compositor, err = bind("wl_compositor", 4); err != nil {
		conn.Close()
		return nil, err
	}
	if d.shm, err = bind("wl_shm", 1); err != nil {
		conn.Close()
		return nil, err
	}
	if d.layerShell, err = bind("zwlr_layer_shell_v1", 4); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w — mavor's overlay needs a compositor with wlr-layer-shell, such as sway, hyprland or river", err)
	}
	if err := conn.Roundtrip(); err != nil {
		conn.Close()
		return nil, err
	}
	return d, nil
}

// Close tears down the connection.
func (d *Display) Close() error { return d.conn.Close() }

// Dispatch runs one round of event handling.
func (d *Display) Dispatch() error { return d.conn.Dispatch() }

// Roundtrip blocks until the compositor has caught up.
func (d *Display) Roundtrip() error { return d.conn.Roundtrip() }

// Surface is a layer-shell surface: a window that the compositor positions by
// anchor and margin rather than by the user, and that never takes focus.
type Surface struct {
	d       *Display
	surface ObjectID
	layer   ObjectID

	// Configured reports the size the compositor assigned, which for an
	// anchored surface may differ from what was requested.
	Width, Height int
	Closed        bool

	configured bool
	pendingAck uint32
	hasAck     bool
}

// NewSurface creates an anchored layer surface of the requested size. The
// caller must Roundtrip before painting: the compositor assigns the final size
// in a configure event, and attaching a buffer before acking it is a protocol
// error.
func (d *Display) NewSurface(namespace string, layer Layer, anchor uint32, width, height int) (*Surface, error) {
	s := &Surface{d: d, Width: width, Height: height}

	s.surface = d.conn.newID(nil)
	// wl_compositor.create_surface(id:new_id)
	b := newBuilder(d.compositor, 0)
	b.putObject(s.surface)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}

	s.layer = d.conn.newID(func(opcode uint16, r *reader) error {
		switch opcode {
		case 0: // configure(serial, width, height)
			serial := r.uint()
			w := r.uint()
			h := r.uint()
			if w > 0 {
				s.Width = int(w)
			}
			if h > 0 {
				s.Height = int(h)
			}
			s.pendingAck, s.hasAck, s.configured = serial, true, true
		case 1: // closed()
			s.Closed = true
		}
		return nil
	})

	// zwlr_layer_shell_v1.get_layer_surface(id, surface, output, layer, namespace)
	// A null output lets the compositor choose, which is what we want.
	b = newBuilder(d.layerShell, 0)
	b.putObject(s.layer)
	b.putObject(s.surface)
	b.putObject(0)
	b.putUint(uint32(layer))
	b.putString(namespace)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}

	// zwlr_layer_surface_v1.set_size(width:uint, height:uint)
	b = newBuilder(s.layer, 0)
	b.putUint(uint32(width))
	b.putUint(uint32(height))
	if err := d.conn.send(b); err != nil {
		return nil, err
	}

	// zwlr_layer_surface_v1.set_anchor(anchor:uint)
	b = newBuilder(s.layer, 1)
	b.putUint(anchor)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}

	// zwlr_layer_surface_v1.set_keyboard_interactivity(mode:uint) — none, so
	// the overlay can never steal focus from whatever you are dictating into.
	b = newBuilder(s.layer, 4)
	b.putUint(0)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}

	if err := s.Commit(); err != nil {
		return nil, err
	}
	return s, nil
}

// SetMargin offsets the surface from the edges it is anchored to.
func (s *Surface) SetMargin(top, right, bottom, left int) error {
	// zwlr_layer_surface_v1.set_margin(top:int, right:int, bottom:int, left:int)
	b := newBuilder(s.layer, 3)
	b.putInt(int32(top))
	b.putInt(int32(right))
	b.putInt(int32(bottom))
	b.putInt(int32(left))
	return s.d.conn.send(b)
}

// Commit applies everything staged on the surface, acking any configure the
// compositor has sent since the last commit.
func (s *Surface) Commit() error {
	if s.hasAck {
		// zwlr_layer_surface_v1.ack_configure(serial:uint)
		b := newBuilder(s.layer, 6)
		b.putUint(s.pendingAck)
		if err := s.d.conn.send(b); err != nil {
			return err
		}
		s.hasAck = false
	}
	// wl_surface.commit()
	return s.d.conn.send(newBuilder(s.surface, 6))
}

// WaitConfigure blocks until the compositor has sized the surface.
func (s *Surface) WaitConfigure() error {
	for !s.configured && !s.Closed {
		if err := s.d.conn.Dispatch(); err != nil {
			return err
		}
	}
	return nil
}

// Attach binds a buffer to the surface, marks the whole surface damaged and
// commits, which is the three-step sequence that puts pixels on screen.
func (s *Surface) Attach(buf *Buffer) error {
	// wl_surface.attach(buffer:object, x:int, y:int)
	b := newBuilder(s.surface, 1)
	b.putObject(buf.id)
	b.putInt(0)
	b.putInt(0)
	if err := s.d.conn.send(b); err != nil {
		return err
	}

	// wl_surface.damage_buffer(x:int, y:int, width:int, height:int)
	b = newBuilder(s.surface, 9)
	b.putInt(0)
	b.putInt(0)
	b.putInt(int32(buf.Width))
	b.putInt(int32(buf.Height))
	if err := s.d.conn.send(b); err != nil {
		return err
	}
	return s.Commit()
}

// Destroy releases the surface and its layer-shell role.
func (s *Surface) Destroy() error {
	// zwlr_layer_surface_v1.destroy()
	if err := s.d.conn.send(newBuilder(s.layer, 7)); err != nil {
		return err
	}
	s.d.conn.forget(s.layer)
	// wl_surface.destroy()
	if err := s.d.conn.send(newBuilder(s.surface, 0)); err != nil {
		return err
	}
	s.d.conn.forget(s.surface)
	return nil
}

// Buffer is a shared-memory image the compositor reads pixels out of. Pix is
// the mapped memory itself, in premultiplied ARGB8888, little-endian — so a
// pixel is B, G, R, A in byte order.
type Buffer struct {
	id            ObjectID
	Width, Height int
	Stride        int
	Pix           []byte

	pool ObjectID
	file *os.File
}

// NewBuffer allocates a shared-memory buffer of the given size and hands the
// compositor a descriptor for it.
func (d *Display) NewBuffer(width, height int) (*Buffer, error) {
	stride := width * 4
	size := stride * height

	f, pix, err := allocShared(size)
	if err != nil {
		return nil, err
	}

	buf := &Buffer{Width: width, Height: height, Stride: stride, Pix: pix, file: f}

	buf.pool = d.conn.newID(nil)
	// wl_shm.create_pool(id:new_id, fd:fd, size:int)
	b := newBuilder(d.shm, 0)
	b.putObject(buf.pool)
	b.putFD(int(f.Fd()))
	b.putInt(int32(size))
	if err := d.conn.send(b); err != nil {
		buf.Close()
		return nil, err
	}

	buf.id = d.conn.newID(nil)
	// wl_shm_pool.create_buffer(id, offset:int, width:int, height:int, stride:int, format:uint)
	b = newBuilder(buf.pool, 0)
	b.putObject(buf.id)
	b.putInt(0)
	b.putInt(int32(width))
	b.putInt(int32(height))
	b.putInt(int32(stride))
	b.putUint(formatARGB8888)
	if err := d.conn.send(b); err != nil {
		buf.Close()
		return nil, err
	}

	// The pool can be released as soon as the buffer exists; the mapping and
	// the buffer both stay valid.
	if err := d.conn.send(newBuilder(buf.pool, 1)); err != nil { // wl_shm_pool.destroy()
		buf.Close()
		return nil, err
	}
	d.conn.forget(buf.pool)

	return buf, nil
}

// Close unmaps the buffer and closes its descriptor.
func (b *Buffer) Close() error {
	var err error
	if b.Pix != nil {
		err = unmapShared(b.Pix)
		b.Pix = nil
	}
	if b.file != nil {
		if cerr := b.file.Close(); err == nil {
			err = cerr
		}
		b.file = nil
	}
	return err
}

// Resize asks the compositor for a new surface size and waits for it to
// confirm. A layer surface must be re-configured before a differently-sized
// buffer may be attached.
func (s *Surface) Resize(width, height int) error {
	// zwlr_layer_surface_v1.set_size(width:uint, height:uint)
	b := newBuilder(s.layer, 0)
	b.putUint(uint32(width))
	b.putUint(uint32(height))
	if err := s.d.conn.send(b); err != nil {
		return err
	}
	s.configured = false
	if err := s.Commit(); err != nil {
		return err
	}
	return s.WaitConfigure()
}

// AttachNothing unmaps the surface. Attaching a null buffer is the protocol's
// way of taking a surface off screen without destroying it, which is what the
// overlay does between dictations.
func (s *Surface) AttachNothing() error {
	// wl_surface.attach(buffer:object, x:int, y:int) with a null buffer
	b := newBuilder(s.surface, 1)
	b.putObject(0)
	b.putInt(0)
	b.putInt(0)
	if err := s.d.conn.send(b); err != nil {
		return err
	}
	return s.Commit()
}
