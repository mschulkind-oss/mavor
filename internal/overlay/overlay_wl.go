package overlay

import (
	"errors"
	"fmt"
	"image"
	"log/slog"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// WL is the overlay drawn directly onto a wlr-layer-shell surface.
//
// One goroutine owns the Wayland connection for its whole life. Every public
// method is a message to that goroutine rather than a direct protocol call,
// because the connection is not safe to drive from two places at once and the
// daemon calls SetLevel from its audio path.
type WL struct {
	log *slog.Logger

	cmds   chan func(*wlState)
	done   chan struct{}
	closed sync.Once
	err    chan error
}

// wlState is everything the render goroutine owns exclusively.
type wlState struct {
	display *wayland.Display
	surface *wayland.Surface
	buf     *wayland.Buffer

	scene  Scene
	levels []float64
	mapped bool
	// reqW/reqH is the size the surface has been asked for. Re-requesting the
	// same size is not a no-op but a hang: the compositor has no reason to
	// send another configure, and the resize would wait for one that never
	// comes.
	reqW, reqH int
	bufW, bufH int
	animate    bool
}

// pulsePeriod matches the CSS keyframes the GTK pill animated on.
const pulsePeriod = 900 * time.Millisecond

// NewWL connects to the compositor and starts the render loop.
func NewWL(topMargin int, log *slog.Logger) (*WL, error) {
	if topMargin < 0 {
		topMargin = 0
	}
	if log == nil {
		log = slog.Default()
	}
	d, err := wayland.Connect()
	if err != nil {
		return nil, err
	}

	// Size is provisional: the surface is resized to fit whatever is being
	// drawn, and the first real frame corrects it.
	w, h, err := SceneSize(Scene{Visual: Recording})
	if err != nil {
		d.Close()
		return nil, err
	}
	s, err := d.NewSurface("mavor", wayland.LayerTop, wayland.AnchorTop, w, h)
	if err != nil {
		d.Close()
		return nil, err
	}
	if err := s.SetMargin(topMargin, 0, 0, 0); err != nil {
		d.Close()
		return nil, err
	}
	if err := s.Commit(); err != nil {
		d.Close()
		return nil, err
	}
	if err := s.WaitConfigure(); err != nil {
		d.Close()
		return nil, err
	}

	o := &WL{
		log:  log,
		cmds: make(chan func(*wlState), 64),
		done: make(chan struct{}),
		err:  make(chan error, 1),
	}
	go o.run(&wlState{
		display: d,
		surface: s,
		levels:  make([]float64, waveCols),
		reqW:    w,
		reqH:    h,
	})
	return o, nil
}

// run owns the connection until Close.
func (o *WL) run(st *wlState) {
	defer func() {
		if st.buf != nil {
			st.buf.Close()
		}
		_ = st.surface.Destroy()
		_ = st.display.Close()
		close(o.done)
	}()

	tick := time.NewTicker(pulsePeriod / 24)
	defer tick.Stop()
	start := time.Now()

	for {
		select {
		case fn, ok := <-o.cmds:
			if !ok {
				return
			}
			fn(st)
			if err := o.paint(st, start); err != nil {
				o.fail(err)
				return
			}
		case <-tick.C:
			if !st.animate {
				continue
			}
			if err := o.paint(st, start); err != nil {
				o.fail(err)
				return
			}
		}
	}
}

func (o *WL) fail(err error) {
	select {
	case o.err <- err:
	default:
	}
	o.log.Error("overlay: wayland loop stopped", "err", err)
}

// paint renders the current scene and puts it on screen.
func (o *WL) paint(st *wlState, start time.Time) error {
	if st.scene.Visual == Hidden {
		if st.mapped {
			// Attaching no buffer is how a surface is unmapped.
			if err := st.surface.AttachNothing(); err != nil {
				return err
			}
			st.mapped = false
		}
		st.animate = false
		return nil
	}
	st.animate = true

	// Phase runs 0..1 and back, matching an alternating CSS animation.
	elapsed := time.Since(start).Seconds()
	cycle := elapsed / pulsePeriod.Seconds()
	phase := cycle - float64(int(cycle))
	if int(cycle)%2 == 1 {
		phase = 1 - phase
	}
	st.scene.Phase = phase

	w, h, err := SceneSize(st.scene)
	if err != nil {
		return err
	}
	if w != st.reqW || h != st.reqH {
		// Every resize is a re-centre, so a preview that grows a character
		// at a time makes the overlay walk sideways. Logged with the text
		// length that caused it, because the two together are the whole
		// explanation for an overlay that will not sit still.
		o.debug("overlay: surface resized",
			"from_w", st.reqW, "from_h", st.reqH, "to_w", w, "to_h", h,
			"preview_chars", len(st.scene.Preview))
		if err := st.surface.Resize(w, h); err != nil {
			return err
		}
		st.reqW, st.reqH = w, h
	}
	// Allocate against the size the compositor assigned, which is what the
	// buffer must match, rather than the size that was asked for.
	sw, sh := st.surface.Width, st.surface.Height
	if st.buf == nil || sw != st.bufW || sh != st.bufH {
		if st.buf != nil {
			st.buf.Close()
			st.buf = nil
		}
		b, err := st.display.NewBuffer(sw, sh)
		if err != nil {
			return err
		}
		st.buf, st.bufW, st.bufH = b, sw, sh
	}

	renderStart := time.Now()
	img, err := Render(st.scene)
	if err != nil {
		return err
	}
	blit(img, st.buf)
	// Render measures and draws every glyph of the preview, so its cost
	// grows with the text. This is the number to look at when the overlay
	// feels like it has lost frames.
	o.debug("overlay: painted",
		"w", sw, "h", sh, "preview_chars", len(st.scene.Preview),
		"render_ms", time.Since(renderStart).Milliseconds(),
		"visual", st.scene.Visual)

	if err := st.surface.Attach(st.buf); err != nil {
		return err
	}
	st.mapped = true
	return nil
}

// blit copies a Go RGBA image into a Wayland shared buffer. Go's RGBA is
// already alpha-premultiplied, which is what ARGB8888 wants, so this is a
// channel reorder rather than a conversion: the wire format is little-endian
// ARGB, so bytes run B, G, R, A.
// The image and the buffer can disagree in size for one frame if the
// compositor assigned something other than what was asked for, so the copy is
// clipped to the overlap and the rest of the buffer cleared.
func blit(img *image.RGBA, buf *wayland.Buffer) {
	for i := range buf.Pix {
		buf.Pix[i] = 0
	}
	h := min(img.Bounds().Dy(), buf.Height)
	w := min(img.Bounds().Dx(), buf.Width)
	for y := 0; y < h; y++ {
		src := img.Pix[y*img.Stride:]
		dst := buf.Pix[y*buf.Stride:]
		for x := 0; x < w; x++ {
			s := src[x*4:]
			d := dst[x*4:]
			d[0] = s[2] // B
			d[1] = s[1] // G
			d[2] = s[0] // R
			d[3] = s[3] // A
		}
	}
}

// post hands work to the render goroutine, failing rather than blocking if the
// loop has stopped.
func (o *WL) post(fn func(*wlState)) error {
	select {
	case <-o.done:
		return errors.New("overlay: closed")
	case o.cmds <- fn:
		return nil
	default:
		// A full queue means the render loop is behind. Dropping a level
		// update is better than stalling the audio path that sent it.
		return nil
	}
}

// Show transitions to a visual state.
func (o *WL) Show(v Visual) error {
	return o.post(func(st *wlState) {
		if v != st.scene.Visual {
			resetWave(st.levels)
			st.scene.Preview = ""
		}
		st.scene.Visual = v
		st.scene.Levels = st.levels
	})
}

// SetLevel appends an audio level to the waveform history.
func (o *WL) SetLevel(level float64) error {
	return o.post(func(st *wlState) {
		// The same fixed-length ring the GTK canvas scrolled, so the
		// scrolling behaviour keeps its existing unit tests.
		shiftWave(st.levels, waveDisplayLevel(level))
		st.scene.Levels = st.levels
	})
}

// SetText updates the partial-transcription preview strip.
func (o *WL) SetText(text string) error {
	return o.post(func(st *wlState) { st.scene.Preview = text })
}

// Close stops the render loop and releases the connection. Idempotent.
func (o *WL) Close() error {
	o.closed.Do(func() { close(o.cmds) })
	<-o.done
	select {
	case err := <-o.err:
		return fmt.Errorf("overlay: %w", err)
	default:
		return nil
	}
}

// debug logs at debug level, tolerating a nil logger. The paint path calls it
// on every frame, so it must never be the thing that panics an overlay.
func (o *WL) debug(msg string, args ...any) {
	if o.log == nil {
		return
	}
	o.log.Debug(msg, args...)
}
