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

	// seat and vkManager are optional: they are what synthetic typing needs,
	// and a compositor may offer the overlay's protocols without them. Zero
	// means absent, and NewVirtualKeyboard says so in words.
	seat      ObjectID
	vkManager ObjectID

	// OutputWidth and OutputHeight are the current mode of the first output
	// the compositor advertises, in pixels. Zero when the compositor offers
	// no wl_output, or advertises none as current — callers must treat zero
	// as "unknown" rather than as a screen of no width.
	//
	// Only the first output is tracked. A layer surface with a null output
	// is placed by the compositor on whichever it likes, so this is a
	// reasonable guess and not a promise; it exists to size a preview
	// sensibly, which is a decision that degrades gracefully when wrong.
	OutputWidth  int
	OutputHeight int
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
	// A seat and a virtual-keyboard manager are optional in the same way as
	// wl_output: the overlay works without them, and only typing does not.
	// Bound here rather than on demand so one connection serves both, and so
	// a compositor that lacks them is discovered at startup where `mavor
	// doctor` can say so.
	if g, ok := found["wl_seat"]; ok {
		id := conn.newID(nil)
		b := newBuilder(registry, 0)
		b.putUint(g.name)
		b.putString("wl_seat")
		b.putUint(min(uint32(7), g.version))
		b.putObject(id)
		if err := conn.send(b); err != nil {
			conn.Close()
			return nil, err
		}
		d.seat = id
	}
	if g, ok := found["zwp_virtual_keyboard_manager_v1"]; ok {
		id := conn.newID(nil)
		b := newBuilder(registry, 0)
		b.putUint(g.name)
		b.putString("zwp_virtual_keyboard_manager_v1")
		b.putUint(min(uint32(1), g.version))
		b.putObject(id)
		if err := conn.send(b); err != nil {
			conn.Close()
			return nil, err
		}
		d.vkManager = id
	}

	// wl_output is optional. The overlay works without it; it only loses the
	// ability to size the preview against the screen, and the caller falls
	// back to a fixed budget.
	if g, ok := found["wl_output"]; ok {
		id := conn.newID(func(opcode uint16, r *reader) error {
			if opcode != 1 { // mode(flags, width, height, refresh)
				return nil
			}
			flags := r.uint()
			w := int(int32(r.uint()))
			h := int(int32(r.uint()))
			_ = r.uint() // refresh
			// Bit 0 is WL_OUTPUT_MODE_CURRENT. An output lists every mode it
			// supports, and only one of them is the one in use.
			if flags&0x1 != 0 && d.OutputWidth == 0 {
				d.OutputWidth, d.OutputHeight = w, h
			}
			return nil
		})
		version := min(uint32(2), g.version)
		b := newBuilder(registry, 0)
		b.putUint(g.name)
		b.putString("wl_output")
		b.putUint(version)
		b.putObject(id)
		if err := conn.send(b); err != nil {
			conn.Close()
			return nil, err
		}
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

// DispatchPending handles what has already arrived and returns immediately.
// Any loop that owns a connection must call it. See Conn.DispatchPending.
func (d *Display) DispatchPending() error { return d.conn.DispatchPending() }

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

	// reqW and reqH are the size most recently ASKED for. A configure whose
	// width or height is zero means "the client decides that dimension", and
	// this is what the client decided — without it a zero leaves Width at
	// whatever it was, which for a growing overlay means a buffer that stays
	// the old size while the scene drawn into it gets bigger.
	reqW, reqH int

	configured bool
	pendingAck uint32
	hasAck     bool
}

// NewSurface creates an anchored layer surface of the requested size. The
// caller must Roundtrip before painting: the compositor assigns the final size
// in a configure event, and attaching a buffer before acking it is a protocol
// error.
func (d *Display) NewSurface(namespace string, layer Layer, anchor uint32, width, height int) (*Surface, error) {
	s := &Surface{d: d, Width: width, Height: height, reqW: width, reqH: height}

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
			// Zero means the compositor is leaving the dimension to us, so
			// the answer is the size we last requested rather than the size
			// we happen to be. sway does exactly this for a surface anchored
			// on one edge only, which is how the overlay is anchored.
			if w > 0 {
				s.Width = int(w)
			} else {
				s.Width = s.reqW
			}
			if h > 0 {
				s.Height = int(h)
			} else {
				s.Height = s.reqH
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

	// An EMPTY input region, so the surface is click-through everywhere.
	//
	// A wl_surface accepts pointer input across its whole extent by default.
	// The overlay is mostly transparent and is about to become much larger
	// than its ink, so without this it would swallow clicks in the region it
	// covers — a status indicator that eats your mouse. Keyboard focus is
	// already refused above; this is the pointer half of the same promise.
	//
	// wl_compositor.create_region(id:new_id), then wl_surface.set_input_region
	// with it. A region with no rectangles added to it is empty.
	region := d.conn.newID(nil)
	b = newBuilder(d.compositor, 1)
	b.putObject(region)
	if err := d.conn.send(b); err != nil {
		return nil, err
	}
	// wl_surface.set_input_region(region:object)
	b = newBuilder(s.surface, 5)
	b.putObject(region)
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
	buf.busy = true
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
	// busy: committed and not yet released. Touched only by the goroutine
	// that owns the connection.
	busy bool
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

	// wl_buffer.release (event 0): the compositor is done with this buffer
	// and it may be drawn into and committed again. Committing one it still
	// holds is a protocol violation, and the answer is a closed connection
	// with no error event — which is exactly how this presented.
	buf.id = d.conn.newID(func(opcode uint16, r *reader) error {
		if opcode == 0 {
			buf.busy = false
		}
		return nil
	})
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
	s.reqW, s.reqH = width, height
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
	return s.waitForSize(width, height)
}

// waitForSize blocks until a configure ARRIVES FOR THIS REQUEST, rather than
// until any configure arrives.
//
// The compositor may already have a configure in flight carrying the previous
// size when set_size is sent. Accepting it satisfies WaitConfigure, returns a
// stale Width, and leaves the surface permanently smaller than the scene being
// drawn into it — the overlay then paints a wide scene into a narrow buffer
// and all the caller sees is a sliver of it. Observed against sway as
// configure(serial=9, 329x56) landing after a request for 960x91, with the
// matching configure(serial=11, 960x91) two events behind it.
//
// A compositor is allowed to impose its own size, so this cannot wait forever
// for an exact match. It gives the right configure a bounded number of
// dispatches to show up and then accepts whatever it has: a surface sized by
// the compositor is legitimate, a surface sized by a stale event is not.
func (s *Surface) waitForSize(width, height int) error {
	if err := s.WaitConfigure(); err != nil {
		return err
	}
	const maxExtraDispatches = 8
	for i := 0; i < maxExtraDispatches; i++ {
		if s.Closed || (s.Width == width && s.Height == height) {
			return nil
		}
		if err := s.d.conn.Dispatch(); err != nil {
			return err
		}
	}
	return nil
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

// Busy reports whether the compositor still holds this buffer.
func (b *Buffer) Busy() bool { return b.busy }
