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

	// wake lets Show reach the render loop without waiting for the next
	// tick, which is what makes it safe for the loop to tick slowly while
	// nothing is on screen.
	//
	// ONLY Show signals it. Waking on a level sample would put the loop back
	// on the recorder's 30ms clock instead of its own 37.5ms one, and since
	// the waveform advances exactly one column per iteration, that is the
	// uneven scroll all over again — see the comment in apply.
	wake chan struct{}
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
	// bufs is a small pool. A committed wl_buffer belongs to the compositor
	// until it releases it, so one buffer cannot be redrawn every frame —
	// three gives it room to hold one while another is being painted.
	bufs   [3]*wayland.Buffer
	bufIdx int

	scene Scene
	// maxPreview is the pixel cap applied to every scene before painting,
	// resolved once at connect from the screen width and the configured
	// fraction.
	maxPreview int
	// img is the scratch the scene is drawn into, kept between frames.
	img *image.RGBA
	// lastLevel is the most recent audio level, carried between frames so a
	// frame that received no sample still scrolls at the same rate.
	lastLevel float64
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

const (
	// frameInterval is the animation frame rate: 24 frames per pulse.
	frameInterval = pulsePeriod / 24

	// idleInterval is how often the loop dispatches while nothing is on
	// screen. It exists only to keep the compositor's events drained — a
	// Wayland client that stops reading is disconnected when the socket
	// fills — and while hidden the only events are the last frame's buffer
	// releases. Half a second is far more often than that needs.
	idleInterval = 500 * time.Millisecond
)

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
		wake: make(chan struct{}, 1),
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
		for _, b := range st.bufs {
			if b != nil {
				b.Close()
			}
		}
		_ = st.surface.Destroy()
		_ = st.display.Close()
		close(o.done)
	}()

	// Two rates. Painting an animation needs the frame rate; sitting hidden
	// needs only enough dispatching to keep the compositor's events drained,
	// and the daemon is hidden for almost all of its life. At the frame rate
	// that was 27 wakeups a second forever — each one blocking a millisecond
	// in a read for events that were not coming — for a surface drawing
	// nothing. Show signals o.wake, so dropping to the idle rate costs no
	// latency when a dictation starts.
	tick := time.NewTicker(frameInterval)
	defer tick.Stop()
	interval := frameInterval
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
		// EXACTLY ONE column per frame, whatever arrived.
		//
		// The recorder samples every 30 ms and this loop paints every
		// 37.5 ms, so a frame receives one sample or two depending on how
		// the two clocks line up. Shifting once per sample therefore
		// advanced the ring by one column on some frames and two on others,
		// which is the stutter — the ring was scrolling at the sampler's
		// rate but being LOOKED at on the painter's.
		//
		// A waveform is read as motion, not as data: even spacing matters
		// more than keeping every sample. One column per frame is uniform by
		// construction. Where two samples arrive, the newer wins and the
		// older is folded in as the peak, so a transient is not lost —
		// silence between two loud samples is the one thing that would look
		// wrong if the newest simply replaced the pair.
		if n := len(d.levels); n > 0 {
			lv := d.levels[n-1]
			if n > 1 {
				for _, older := range d.levels[:n-1] {
					if older > lv {
						lv = older
					}
				}
			}
			st.lastLevel = lv
		}
		st.scene.Levels = st.levels
		st.sceneSetAt = d.setAt
		return true
	}

	for {
		select {
		case <-o.quit:
			return
		case <-o.wake:
		case <-tick.C:
		}

		// Read first. Releases arrive here, and a client that never
		// reads is disconnected once the buffer fills.
		if err := st.display.DispatchPending(); err != nil {
			o.fail(err)
			return
		}
		// A change is reason to paint even when nothing is animating:
		// it is how a state the producer asked for reaches the screen.
		changed := apply()

		// Scroll here rather than in apply: apply returns early when
		// nothing arrived, and a frame with no new sample must still
		// advance the ring by one column or the motion is uneven again.
		// This is the ONLY place the waveform scrolls, so its rate is
		// the frame rate and nothing else.
		if st.scene.Visual == Recording {
			shiftWave(st.levels, waveDisplayLevel(st.lastLevel))
			st.scene.Levels = st.levels
			changed = true
		}
		if st.animate || changed {
			if err := o.paint(st, start); err != nil {
				o.fail(err)
				return
			}
		}

		// paint sets st.animate from the scene it just drew, so this reads
		// the rate the NEXT iteration should run at.
		want := idleInterval
		if st.animate {
			want = frameInterval
		}
		if want != interval {
			interval = want
			tick.Reset(interval)
			o.log.Debug("overlay: tick rate changed", "interval", interval, "animating", st.animate)
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
	// Hidden is drawn, not unmapped.
	//
	// Unmapping (attaching a null buffer) looks like the natural way to hide,
	// and it is a trap: a compositor holding buffers for an unmapped surface
	// has no reason to release them, so every buffer stays busy and every
	// frame of the NEXT dictation is skipped. Destroying them instead is
	// worse — the compositor closes the connection. Re-mapping needs a fresh
	// configure handshake that is easy to get subtly wrong.
	//
	// None of that is necessary. The surface is a fixed size and its input
	// region is empty, so a fully transparent frame is invisible and
	// click-through, and the buffer cycle keeps turning. The surface simply
	// stays mapped for the life of the daemon.
	if st.scene.Visual == Hidden {
		st.animate = false
	} else {
		st.animate = true
	}

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
	if st.bufs[0] == nil || sw != st.bufW || sh != st.bufH {
		for i := range st.bufs {
			if st.bufs[i] != nil {
				st.bufs[i].Close()
				st.bufs[i] = nil
			}
		}
		for i := range st.bufs {
			b, err := st.display.NewBuffer(sw, sh)
			if err != nil {
				return err
			}
			st.bufs[i] = b
		}
		st.bufW, st.bufH = sw, sh
	}

	// Take one the compositor has given back. Skipping a frame is a frame
	// nobody misses; committing a buffer still in use loses the connection.
	var buf *wayland.Buffer
	for range st.bufs {
		st.bufIdx = (st.bufIdx + 1) % len(st.bufs)
		if !st.bufs[st.bufIdx].Busy() {
			buf = st.bufs[st.bufIdx]
			break
		}
	}
	if buf == nil {
		o.debug("overlay: no free buffer this frame — every one still held by the compositor")
		return nil
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
	blit(st.img, buf)
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

	if err := st.surface.Attach(buf); err != nil {
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
	select {
	case o.wake <- struct{}{}:
	default: // already pending; the loop will see the latest state anyway
	}
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
