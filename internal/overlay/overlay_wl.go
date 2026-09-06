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

	// quit is closed by Close. It carries no values: producers write state
	// rather than queueing edits, so the only thing the loop needs from the
	// outside is the signal to stop.
	quit   chan struct{}
	done   chan struct{}
	closed sync.Once
	err    chan error

	// want is the latest state the producers asked for. They write it and
	// return; the render loop reads a snapshot each frame.
	//
	// A queue of edits was the wrong shape. Every one of these values is
	// idempotent — only the newest has meaning — so a queue can only choose
	// between blocking the producer and losing the newest value, and the old
	// code chose to drop. Dropping a level sample is invisible; dropping a
	// Show(Transcribing) is the overlay never changing state, which is
	// exactly what a user reported. Latest-wins has neither failure: nobody
	// blocks, and being overwritten by something newer is what should happen
	// to a superseded value.
	mu      sync.Mutex
	want    desired
	wantSeq uint64
}

// desired is what the producers want on screen. Guarded by WL.mu.
type desired struct {
	visual  Visual
	preview string
	// levels are every sample that arrived since the last frame, oldest
	// first — not just the newest.
	//
	// The waveform is a time history: one column per sample. Keeping only
	// the newest made the ring advance once per FRAME (37.5 ms) while the
	// recorder samples every 30 ms, so it scrolled slower than the audio and
	// by a varying number of columns depending on how the two clocks lined
	// up. That is the jitter. Draining every sample restores one column per
	// sample and a uniform scroll.
	//
	// Bounded: on the impossible frame where thousands arrive, the OLDEST go,
	// because a waveform is about what just happened.
	levels []float64
	// setAt is when the newest of these was written, so a frame can report
	// how long the update waited before it reached the screen.
	setAt time.Time
}

// wlState is everything the render goroutine owns exclusively.
type wlState struct {
	display *wayland.Display
	surface *wayland.Surface
	buf     *wayland.Buffer

	scene Scene
	// maxPreview is the pixel cap applied to every scene before painting,
	// resolved once at connect from the screen width and the configured
	// fraction.
	maxPreview int
	// img is the scratch the scene is drawn into, kept between frames.
	img *image.RGBA
	// sceneSetAt is when the newest update in the current scene was written
	// by its producer, so a frame can report how long it waited.
	sceneSetAt time.Time
	levels     []float64
	mapped     bool
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
// fallbackPreviewWidth caps the preview when the compositor advertises no
// wl_output and the screen width is unknown. Wide enough to be useful, narrow
// enough not to run off a small laptop panel.
const fallbackPreviewWidth = 640

func NewWL(topMargin int, previewFraction float64, log *slog.Logger) (*WL, error) {
	if topMargin < 0 {
		topMargin = 0
	}
	if previewFraction <= 0 || previewFraction > 1 {
		previewFraction = 0.5
	}
	if log == nil {
		log = slog.Default()
	}
	d, err := wayland.Connect()
	if err != nil {
		return nil, err
	}

	maxPreview := fallbackPreviewWidth
	if d.OutputWidth > 0 {
		maxPreview = int(float64(d.OutputWidth) * previewFraction)
	}
	log.Info("overlay: preview width cap",
		"px", maxPreview, "output_width", d.OutputWidth, "fraction", previewFraction)

	// The size, once and for good. Wide enough for the preview cap and tall
	// enough for the strip whether or not one is showing, so no frame ever
	// needs a different surface than the one already on screen.
	w, h, err := FixedSurfaceSize(maxPreview)
	if err != nil {
		d.Close()
		return nil, err
	}
	log.Info("overlay: fixed surface", "w", w, "h", h, "preview_cap_px", maxPreview)
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
		quit: make(chan struct{}),
		done: make(chan struct{}),
		err:  make(chan error, 1),
	}
	go o.run(&wlState{
		display:    d,
		surface:    s,
		levels:     make([]float64, waveCols),
		reqW:       w,
		reqH:       h,
		maxPreview: maxPreview,
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
	var seen uint64

	// apply folds the latest state the producers asked for into the scene
	// this goroutine owns. Producers never touch the scene themselves.
	apply := func() bool {
		d, changed := o.takeDesired(&seen)
		if !changed {
			return false
		}
		if d.visual != st.scene.Visual {
			resetWave(st.levels)
		}
		st.scene.Visual = d.visual
		st.scene.Preview = d.preview
		// One shift per sample, in order: the ring is a time history and a
		// dropped sample is a missing column.
		for _, lv := range d.levels {
			shiftWave(st.levels, waveDisplayLevel(lv))
		}
		st.scene.Levels = st.levels
		st.sceneSetAt = d.setAt
		return true
	}

	for {
		select {
		case <-o.quit:
			return
		case <-tick.C:
			// A change is reason to paint even when nothing is animating:
			// it is how a state the producer asked for reaches the screen.
			changed := apply()
			if !st.animate && !changed {
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
	st.scene.MaxPreviewWidth = st.maxPreview
	st.scene.SurfaceW, st.scene.SurfaceH = st.reqW, st.reqH

	w, h, err := SceneSize(st.scene)
	if err != nil {
		return err
	}
	// No resize. The canvas was fixed at startup and the scene is laid out
	// inside it, which is the whole design: a resize re-centres the surface,
	// blocks this loop on a compositor round-trip, and can race a stale
	// configure. If this ever fires, something has un-pinned the scene.
	if w != st.reqW || h != st.reqH {
		o.log.Warn("overlay: scene wants a size the fixed surface does not have",
			"scene_w", w, "scene_h", h, "surface_w", st.reqW, "surface_h", st.reqH)
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
	// Sized from the SCENE, not from the surface. Resize is asynchronous —
	// the compositor has not acked the new size on the frame that asks for
	// it — so on exactly that frame the surface is still the old, smaller
	// one. Sizing the scratch from it produced a buffer too small for the
	// scene, and the error killed the whole render loop: the overlay
	// vanished mid-recording while dictation carried on.
	//
	// blit clips to whichever is smaller, so a scene bigger than the surface
	// is drawn correctly and simply cropped until the resize lands.
	if st.img == nil || st.img.Bounds().Dx() < w || st.img.Bounds().Dy() < h {
		st.img = image.NewRGBA(image.Rect(0, 0, w, h))
	}
	if err := RenderInto(st.img, st.scene); err != nil {
		// Never fatal. A frame mavor cannot draw is a frame the user does
		// not see; a render loop that stops is an overlay that never comes
		// back, and the daemon goes on recording behind it.
		o.debug("overlay: frame skipped", "err", err)
		return nil
	}
	blit(st.img, st.buf)
	// Render measures and draws every glyph of the preview, so its cost
	// grows with the text. This is the number to look at when the overlay
	// feels like it has lost frames.
	// age is how long the newest update waited between a producer writing it
	// and it reaching the screen. It is the number that would have named the
	// blocking-resize stall on sight, so it is logged on every frame.
	var ageMS int64
	if !st.sceneSetAt.IsZero() {
		ageMS = time.Since(st.sceneSetAt).Milliseconds()
	}
	o.debug("overlay: painted",
		"w", sw, "h", sh, "preview_chars", len(st.scene.Preview),
		"render_us", time.Since(renderStart).Microseconds(),
		"update_age_ms", ageMS,
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

// Show transitions to a visual state. Never dropped: it is recorded as the
// latest wanted state, and the next frame paints it.
func (o *WL) Show(v Visual) error {
	select {
	case <-o.done:
		return errors.New("overlay: closed")
	default:
	}
	o.mu.Lock()
	if v != o.want.visual {
		// A state change starts the preview over: the text belonged to the
		// state being left.
		o.want.preview = ""
	}
	o.want.visual = v
	o.want.setAt = time.Now()
	o.wantSeq++
	o.mu.Unlock()
	return nil
}

// showLegacy is the old queued path, kept only for the fields the render loop

// SetText updates the partial-transcription preview strip.
// SetLevel records the newest audio level. Never dropped and never blocking:
// the audio path must not wait on the overlay.
func (o *WL) SetLevel(level float64) error {
	select {
	case <-o.done:
		return errors.New("overlay: closed")
	default:
	}
	o.mu.Lock()
	o.want.levels = append(o.want.levels, level)
	if n := len(o.want.levels); n > maxPendingLevels {
		o.want.levels = append(o.want.levels[:0], o.want.levels[n-maxPendingLevels:]...)
	}
	o.want.setAt = time.Now()
	o.wantSeq++
	o.mu.Unlock()
	return nil
}

// maxPendingLevels is a couple of seconds of samples at the recorder's 30 ms
// cadence — far more than a frame should ever hold, and a bound rather than a
// target.
const maxPendingLevels = 64

func (o *WL) SetText(text string) error {
	select {
	case <-o.done:
		return errors.New("overlay: closed")
	default:
	}
	o.mu.Lock()
	o.want.preview = text
	o.want.setAt = time.Now()
	o.wantSeq++
	o.mu.Unlock()
	return nil
}

// takeDesired snapshots what the producers last asked for, and reports whether
// anything changed since the render loop last looked.
func (o *WL) takeDesired(seen *uint64) (desired, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d := o.want
	changed := o.wantSeq != *seen
	*seen = o.wantSeq
	// The caller now owns the samples; start a fresh batch rather than
	// handing out the same slice twice.
	o.want.levels = nil
	return d, changed
}

// Close stops the render loop and releases the connection. Idempotent.
func (o *WL) Close() error {
	o.closed.Do(func() { close(o.quit) })
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
