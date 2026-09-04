//go:build cgo && !nogtk

package overlay

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/diamondburned/gotk4-layer-shell/pkg/gtk4layershell"
	"github.com/diamondburned/gotk4/pkg/cairo"
	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// GTK is the production overlay implementation. Layout:
//
//	┌─────────────────────────────────────────┐
//	│  ●  RECORDING  ▁▃▂▅▁▃                  │  ← red floating pill, centered
//	└─────────────────────────────────────────┘
//	┌─────────────────────────────────────────┐
//	│  incoming live streaming text preview   │  ← .mavor-preview subtitle strip
//	└─────────────────────────────────────────┘
//
// The window is anchored Top (with topMargin below Waybar) and centered
// horizontally. Transparent window background ensures no white wings.
type GTK struct {
	app          *gtk.Application
	window       *gtk.Window
	stack        *gtk.Stack
	waveform     *gtk.DrawingArea
	wave         []float64 // time-scroll history: index 0 = oldest, last = newest
	previewLabel *gtk.Label
	topMargin    int

	mu       sync.Mutex
	current  Visual
	text     string
	closed   bool
	mainDone chan struct{}
}

// CSS for the overlay pill, subtitle preview, and transparent window container.
const overlayCSS = `
window,
window.background,
window.csd,
window.solid-csd,
.background,
.csd,
.solid-csd,
decoration,
.mavor-window,
.mavor-container,
stack {
  background: transparent;
  background-color: transparent;
  box-shadow: none;
  border: none;
  outline: none;
}

.mavor-bar {
  padding: $PAD_Y $PAD_X;
  font-family: "Inter", "Cantarell", sans-serif;
  font-size: 15px;
  font-weight: 700;
  letter-spacing: 0.08em;
  color: white;
  border-radius: 20px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.4), inset 0 1px 0 rgba(255, 255, 255, 0.25);
  margin: 0;
}

/* RECORDING — deep red, pulsing dot, and time-scroll waveform (canvas). */
.mavor-bar.mavor-recording {
  background: linear-gradient(180deg, #c81818, #8a0000);
}
.mavor-rec-dot {
  animation: mavor-pulse 0.9s ease-in-out infinite alternate;
  margin-right: 10px;
}
@keyframes mavor-pulse {
  from { opacity: 0.35; transform: scale(0.85); }
  to   { opacity: 1.0;  transform: scale(1.15); }
}
.mavor-wave {
  margin-left: 12px;
}

/* TRANSCRIBING — amber, typing-dots indicator. No waveform: nothing is
   being recorded in this state, only the tail is being transcribed. */
.mavor-bar.mavor-transcribing {
  background: linear-gradient(180deg, #d68910, #7a4807);
}
.mavor-typing {
  margin-left: 12px;
}
.mavor-typing-dot {
  color: #ffe0a8;
  font-size: 18px;
  line-height: 1;
  animation: mavor-typing 1.1s ease-in-out infinite;
}
.mavor-typing-dot:nth-child(1) { animation-delay: 0.00s; }
.mavor-typing-dot:nth-child(2) { animation-delay: 0.18s; }
.mavor-typing-dot:nth-child(3) { animation-delay: 0.36s; }
@keyframes mavor-typing {
  0%, 60%, 100% { opacity: 0.25; }
  30%           { opacity: 1.0; }
}

/* ERROR — deep crimson warning bar. */
.mavor-bar.mavor-error {
  background: linear-gradient(180deg, #a80000, #5c0000);
}
.mavor-err-icon {
  font-size: 18px;
  color: #ffcccc;
  margin-right: 10px;
}

/* SUBTITLE PREVIEW — sleek translucent strip below the recording pill. */
.mavor-preview {
  padding: 6px 18px;
  font-family: "Inter", "Cantarell", sans-serif;
  font-size: 13px;
  font-weight: 600;
  letter-spacing: 0.03em;
  color: #f3f4f6;
  background: rgba(15, 23, 42, 0.85);
  border: 1px solid rgba(255, 255, 255, 0.15);
  border-radius: 14px;
  box-shadow: 0 4px 14px rgba(0, 0, 0, 0.35);
  margin-top: 6px;
}
`

// The GTK application is process-wide and outlives any single overlay. GTK
// cannot be torn down and re-initialized: a binary that runs a second
// gtk.Application takes a SIGTERM seconds later. The daemon only ever builds
// one overlay, but the tests build several, and a constructor that kills the
// process the second time it is called is a trap either way.
var (
	sharedAppOnce  sync.Once
	sharedApp      *gtk.Application
	sharedAppErr   error
	sharedAppReady = make(chan struct{})
	sharedAppDone  = make(chan struct{})
)

// startSharedApp brings up the GTK application and its main loop once, and
// leaves both running for the life of the process.
func startSharedApp() {
	sharedAppOnce.Do(func() {
		go func() {
			app := gtk.NewApplication("dev.mavor.overlay", gio.ApplicationNonUnique)
			sharedApp = app

			// Hold a reference so the application does not quit when an
			// overlay closes its last window — the next NewGTK needs the
			// loop still running.
			app.Hold()

			app.ConnectActivate(func() { close(sharedAppReady) })

			if code := app.Run(nil); code != 0 {
				sharedAppErr = fmt.Errorf("overlay: gtk application exited with code %d", code)
			}
			close(sharedAppDone)
		}()
	})
}

// Shutdown quits the process-wide GTK application and waits for its main loop
// to end. It is not needed to close an overlay — Close does that — but a host
// that tears down the compositor itself must call this first: a GTK main loop
// whose compositor disappears takes the process down with it.
//
// Safe to call when no application was ever started, and safe to call twice.
func Shutdown() {
	select {
	case <-sharedAppReady:
	default:
		return // never started; nothing to quit
	}

	if app := sharedApp; app != nil {
		coreglib.IdleAdd(func() { app.Quit() })
	}
	<-sharedAppDone
}

func NewGTK(topMargin int) (*GTK, error) {
	if topMargin < 0 {
		return nil, fmt.Errorf("overlay: topMargin must be >= 0, got %d", topMargin)
	}

	startSharedApp()
	select {
	case <-sharedAppReady:
	case <-sharedAppDone:
		if sharedAppErr != nil {
			return nil, sharedAppErr
		}
		return nil, errors.New("overlay: gtk application exited before activating")
	}

	g := &GTK{topMargin: topMargin, app: sharedApp, mainDone: sharedAppDone}

	// The widget tree has to be built on the GTK thread.
	built := make(chan error, 1)
	coreglib.IdleAdd(func() { built <- g.buildUI() })
	select {
	case err := <-built:
		if err != nil {
			return nil, err
		}
	case <-sharedAppDone:
		return nil, errors.New("overlay: gtk application exited while building the overlay")
	}
	return g, nil
}

func (g *GTK) buildUI() error {
	if !gtk4layershell.IsSupported() {
		return errors.New("overlay: layer-shell protocol not available on this compositor")
	}

	// Inject CSS at USER priority (800) so it overrides system GTK themes
	// (e.g. Adwaita default white window background).
	provider := gtk.NewCSSProvider()
	provider.LoadFromString(styleSheet())
	if disp := gdk.DisplayGetDefault(); disp != nil {
		gtk.StyleContextAddProviderForDisplay(disp, provider, gtk.STYLE_PROVIDER_PRIORITY_USER)
	}

	w := gtk.NewApplicationWindow(g.app)
	g.window = &w.Window
	g.window.AddCSSClass("mavor-window")

	gtk4layershell.InitForWindow(g.window)
	gtk4layershell.SetLayer(g.window, gtk4layershell.LayerShellLayerTop)
	gtk4layershell.SetAnchor(g.window, gtk4layershell.LayerShellEdgeTop, true)
	gtk4layershell.SetAnchor(g.window, gtk4layershell.LayerShellEdgeLeft, false)
	gtk4layershell.SetAnchor(g.window, gtk4layershell.LayerShellEdgeRight, false)
	gtk4layershell.SetAnchor(g.window, gtk4layershell.LayerShellEdgeBottom, false)
	gtk4layershell.SetMargin(g.window, gtk4layershell.LayerShellEdgeTop, g.topMargin)
	gtk4layershell.SetKeyboardMode(g.window, gtk4layershell.LayerShellKeyboardModeNone)
	// NO SetExclusiveZone: float over content, do not push windows around.

	g.stack = gtk.NewStack()
	g.stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)
	g.stack.SetTransitionDuration(180)
	g.stack.AddNamed(g.buildRecordingChild(), "recording")
	g.stack.AddNamed(buildTranscribingChild(), "transcribing")
	g.stack.AddNamed(buildErrorChild(), "error")

	g.previewLabel = gtk.NewLabel("")
	g.previewLabel.AddCSSClass("mavor-preview")
	g.previewLabel.SetHAlign(gtk.AlignCenter)
	g.previewLabel.SetVisible(false)

	container := gtk.NewBox(gtk.OrientationVertical, 0)
	container.AddCSSClass("mavor-container")
	container.SetHAlign(gtk.AlignCenter)
	container.Append(g.stack)
	container.Append(g.previewLabel)

	g.window.SetChild(container)
	g.window.SetTitle("mavor")
	g.window.SetDecorated(false)
	g.window.SetResizable(false)
	return nil
}

// staticCSS freezes the pulsing dot and typing dots on their first keyframe.
// Screenshot capture otherwise catches them at whatever phase the animation
// happens to be in, so regenerating the storybook rewrites every PNG whether
// or not the UI changed.
const staticCSS = `
.mavor-rec-dot { animation: none; opacity: 1; }
.mavor-typing-dot { animation: none; opacity: 1; }
`

// styleSheet returns the overlay stylesheet, with animations disabled when
// MAVOR_OVERLAY_STATIC=1 so captures are reproducible. The pill's padding is
// interpolated from the Go constants so the stylesheet and the geometry the
// tests assert against cannot drift apart.
func styleSheet() string {
	// Token substitution rather than Sprintf: the stylesheet's @keyframes
	// blocks are full of bare percent signs that a format string would eat.
	css := strings.NewReplacer(
		"$PAD_Y", fmt.Sprintf("%dpx", barPaddingY),
		"$PAD_X", fmt.Sprintf("%dpx", barPaddingX),
	).Replace(overlayCSS)
	if os.Getenv("MAVOR_OVERLAY_STATIC") == "1" {
		return css + staticCSS
	}
	return css
}

// Waveform column layout. These are drawing-only: how many columns the canvas
// holds and how wide each is. The heights the pill's proportions depend on
// live in overlay.go, where the tests can reach them without a GTK build.
const (
	// waveCols is how many samples the trace holds. At the daemon's ~33 Hz
	// level cadence this is about 1.4 seconds of history.
	waveCols     = 46
	waveColWidth = 3 // 2 px bar + 1 px gap
	waveBarWidth = 2
)

// recordingLabel is the pill's text. Shared with labelOpticalRise so the dot is
// aligned against the string that is actually rendered.
const recordingLabel = "RECORDING"

// capHeightRef is measured instead of the label itself because round capitals
// overshoot: the O, C, G and D in "RECORDING" are drawn a fraction above the
// cap line and below the baseline so they look the same size as the flat
// letters, and Pango's ink rect reports that overshoot. Measuring it would put
// the cap centre lower than the eye reads it. "H" is flat top and bottom, so
// its ink rect is the cap box exactly.
const capHeightRef = "H"

// labelOpticalRise measures how far the label's capitals ride above the centre
// of the line box they are centred in, in the font the stylesheet actually
// resolved. Returns 0 if Pango cannot report extents, which leaves the dot
// geometrically centred — the behaviour before this correction existed.
func labelOpticalRise(label *gtk.Label) float64 {
	layout := label.CreatePangoLayout(capHeightRef)
	if layout == nil {
		return 0
	}
	ink, logical := layout.PixelExtents()
	if ink == nil || logical == nil || ink.Height() == 0 || logical.Height() == 0 {
		return 0
	}
	return opticalRise(ink.Y(), ink.Height(), logical.Y(), logical.Height())
}

func (g *GTK) buildRecordingChild() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.SetHAlign(gtk.AlignCenter)
	box.AddCSSClass("mavor-bar")
	box.AddCSSClass("mavor-recording")

	label := gtk.NewLabel(recordingLabel)
	label.SetVAlign(gtk.AlignCenter)

	// Drawn, not typeset. As a text glyph the dot was baseline-aligned
	// against smaller capitals and sat 2.5px low, by a margin that shifted
	// with the resolved font.
	dot := gtk.NewDrawingArea()
	dot.AddCSSClass("mavor-rec-dot")
	dot.SetContentWidth(recDotBoxW)
	dot.SetContentHeight(recDotBoxH)
	dot.SetVAlign(gtk.AlignCenter)
	dot.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		// Measured here rather than at construction: the label only carries
		// the stylesheet's font once it has been styled, and the draw
		// function first runs well after that.
		cx, cy, r := dotCircle(w, h, labelOpticalRise(label))
		cr.SetSourceRGBA(1.0, 0.878, 0.878, 1.0) // warm white #ffe0e0
		cr.NewPath()
		cr.Arc(cx, cy, r, 0, 2*math.Pi)
		cr.Fill()
	})
	box.Append(dot)
	box.Append(label)

	// Time-scroll waveform: one column per sample, newest at the right edge,
	// history scrolling left. Drawn on a canvas rather than with widget bars
	// so each column keeps its own height instead of the whole meter resizing
	// in lockstep.
	g.wave = make([]float64, waveCols)
	area := gtk.NewDrawingArea()
	area.AddCSSClass("mavor-wave")
	area.SetContentWidth(waveCols * waveColWidth)
	area.SetContentHeight(waveHeight)
	area.SetVAlign(gtk.AlignCenter)
	area.SetDrawFunc(func(_ *gtk.DrawingArea, cr *cairo.Context, w, h int) {
		g.drawWave(cr, w, h)
	})
	g.waveform = area
	box.Append(area)
	return box
}

func buildTranscribingChild() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.SetHAlign(gtk.AlignCenter)
	box.AddCSSClass("mavor-bar")
	box.AddCSSClass("mavor-transcribing")
	box.Append(gtk.NewLabel("TRANSCRIBING…"))

	// Typing-dots "working" indicator. Deliberately no waveform: the
	// transcribing state means no audio is being captured, only the tail
	// of what was already recorded is being transcribed.
	dots := gtk.NewBox(gtk.OrientationHorizontal, 4)
	dots.AddCSSClass("mavor-typing")
	dots.SetVAlign(gtk.AlignCenter)
	for range 3 {
		d := gtk.NewLabel("•")
		d.AddCSSClass("mavor-typing-dot")
		dots.Append(d)
	}
	box.Append(dots)
	return box
}

func buildErrorChild() *gtk.Box {
	box := gtk.NewBox(gtk.OrientationHorizontal, 0)
	box.SetHAlign(gtk.AlignCenter)
	box.AddCSSClass("mavor-bar")
	box.AddCSSClass("mavor-error")

	icon := gtk.NewLabel("⚠")
	icon.AddCSSClass("mavor-err-icon")
	box.Append(icon)
	box.Append(gtk.NewLabel("ERROR"))
	return box
}

// SetLevel appends one amplitude sample to the waveform's time history and
// schedules a redraw. Safe to call from any goroutine; samples arrive at the
// daemon's ~33 Hz level cadence.
func (g *GTK) SetLevel(level float64) error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("overlay closed")
	}
	if level < 0 {
		level = 0
	} else if level > 1 {
		level = 1
	}
	// Shift history one slot left; the newest sample lands at the right edge.
	shiftWave(g.wave, level)
	g.mu.Unlock()

	coreglib.IdleAdd(func() {
		if g.waveform != nil {
			g.waveform.QueueDraw()
		}
	})
	return nil
}

// Wave returns a snapshot of the waveform's time-history ring (index 0 =
// oldest, last = newest). Test-friendly helper mirroring Noop.Levels.
func (g *GTK) Wave() []float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]float64(nil), g.wave...)
}

// drawWave renders the time-scroll waveform onto the canvas. samples[0] is
// the oldest, the last sample is the newest (right edge). Each column is a
// bar symmetric about the vertical center, with heights passed through
// waveDisplayLevel so typical speech levels fill a visible share of the
// canvas; older columns fade so activity reads as fresh at the live edge.
func (g *GTK) drawWave(cr *cairo.Context, w, h int) {
	g.mu.Lock()
	samples := make([]float64, len(g.wave))
	copy(samples, g.wave)
	g.mu.Unlock()

	n := len(samples)
	if n == 0 {
		return
	}
	mid := float64(h) / 2

	for i, lvl := range samples {
		t := float64(i) / float64(n-1)
		alpha := 0.3 + 0.7*t
		r, gr, b := 1.0, 0.878, 0.878 // warm white #ffe0e0
		if i == n-1 {
			r, gr, b = 1.0, 1.0, 1.0 // live edge at full white
		}
		bh := float64(waveBarHeight(waveDisplayLevel(lvl), h))
		x := float64(i) * waveColWidth
		cr.SetSourceRGBA(r, gr, b, alpha)
		cr.NewPath()
		cr.Rectangle(x, mid-bh/2, waveBarWidth, bh)
		cr.Fill()
	}
}

// SetText updates the live partial token transcription preview subtitle text.
func (g *GTK) SetText(text string) error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("overlay closed")
	}
	g.text = text
	g.mu.Unlock()

	coreglib.IdleAdd(func() {
		g.mu.Lock()
		txt := g.text
		g.mu.Unlock()

		if g.previewLabel == nil {
			return
		}
		if txt == "" {
			g.previewLabel.SetText("")
			g.previewLabel.SetVisible(false)
		} else {
			g.previewLabel.SetText(txt)
			g.previewLabel.SetVisible(true)
		}
	})
	return nil
}

// Show transitions to the given visual state. Hidden unmaps the window.
func (g *GTK) Show(v Visual) error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return errors.New("overlay closed")
	}
	g.current = v
	if v == Recording {
		// Clear the trace synchronously, under the same lock SetLevel takes.
		// Doing it in the idle callback below raced with the level samples
		// that arrive as soon as Show returns: the callback ran after the
		// first samples had landed and wiped them, blanking the start of the
		// trace. At the daemon's ~33 Hz cadence that was the better part of a
		// second of waveform, lost every time recording began.
		resetWave(g.wave)
	}
	g.mu.Unlock()

	coreglib.IdleAdd(func() {
		switch v {
		case Hidden:
			g.window.SetVisible(false)
			if g.previewLabel != nil {
				g.previewLabel.SetText("")
				g.previewLabel.SetVisible(false)
			}
		case Recording:
			// The ring was already cleared above; this only repaints it.
			if g.waveform != nil {
				g.waveform.QueueDraw()
			}
			g.stack.SetVisibleChildName("recording")
			g.window.Present()
		case Transcribing:
			g.stack.SetVisibleChildName("transcribing")
			if g.previewLabel != nil {
				g.previewLabel.SetText("")
				g.previewLabel.SetVisible(false)
			}
			g.window.Present()
		case Error:
			g.stack.SetVisibleChildName("error")
			if g.previewLabel != nil {
				g.previewLabel.SetText("")
				g.previewLabel.SetVisible(false)
			}
			g.window.Present()
		}
	})
	return nil
}

// Close releases the GTK application and destroys the window.
func (g *GTK) Close() error {
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return nil
	}
	g.closed = true
	g.mu.Unlock()

	// Destroy the window but leave the application running: it is shared
	// with every other overlay in this process, and quitting it would make a
	// later NewGTK impossible.
	destroyed := make(chan struct{})
	coreglib.IdleAdd(func() {
		if g.window != nil {
			g.window.Destroy()
			g.window = nil
		}
		close(destroyed)
	})
	select {
	case <-destroyed:
	case <-g.mainDone:
	}
	return nil
}
