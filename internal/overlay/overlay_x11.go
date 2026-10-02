package overlay

import (
	"context"
	"errors"
	"golang.org/x/image/draw"
	"image"
	"log/slog"
	"sync"
	"time"
)

// X11 owns a passive, alpha XWayland window. It never requests focus or input.
// Producer state is bounded/latest-wins; only run uses protocol resources.
type X11 struct {
	mu               sync.Mutex
	want             desired
	revision         uint64
	closing          bool
	quit, done, wake chan struct{}
	once             sync.Once
	transport        *xTransport
	margin           int
	fraction         float64
	log              *slog.Logger
	frames           frameStore
}

func NewX11(margin int, fraction float64, log *slog.Logger) (*X11, error) {
	margin = max(0, margin)
	if fraction <= 0 || fraction > 1 {
		fraction = .5
	}
	if log == nil {
		log = slog.Default()
	}
	t, err := connectX11(margin, fraction)
	if err != nil {
		return nil, err
	}
	o := &X11{transport: t, margin: margin, fraction: fraction, log: log, quit: make(chan struct{}), done: make(chan struct{}), wake: make(chan struct{}, 1)}
	o.log.Info("overlay: passive GNOME window verified", "window", uint32(t.win), "input_rectangles", 0, "scale", t.scale, "screen", t.rect)
	go o.run(t)
	return o, nil
}
func (o *X11) mutate(change func(*desired)) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closing {
		return errors.New("overlay: closed")
	}
	change(&o.want)
	o.want.setAt = time.Now()
	o.revision++
	return nil
}
func (o *X11) Show(v Visual) error {
	if v < Hidden || v > Degraded {
		return errors.New("overlay: invalid visual")
	}
	err := o.mutate(func(d *desired) {
		if v != d.visual {
			d.preview = ""
			d.levels = nil
		}
		d.visual = v
	})
	if err == nil {
		select {
		case o.wake <- struct{}{}:
		default:
		}
	}
	return err
}
func (o *X11) SetText(text string) error {
	return o.mutate(func(d *desired) {
		if d.visual == Recording || d.visual == Initializing || d.visual == Error || d.visual == Degraded {
			d.preview = text
		}
	})
}
func (o *X11) SetLevel(level float64) error {
	return o.mutate(func(d *desired) {
		d.levels = append(d.levels, level)
		if n := len(d.levels); n > maxPendingLevels {
			copy(d.levels, d.levels[n-maxPendingLevels:])
			d.levels = d.levels[:maxPendingLevels]
		}
	})
}
func (o *X11) SyncFrame(ctx context.Context) (FrameReceipt, error) {
	o.mu.Lock()
	revision := o.revision
	closed := o.closing
	transport := o.transport
	o.mu.Unlock()
	if closed {
		return FrameReceipt{}, errors.New("overlay: closed")
	}
	if transport != nil {
		select {
		case <-transport.lost:
			return FrameReceipt{}, errors.New("overlay: XWayland connection lost")
		default:
		}
	}
	return o.frames.wait(ctx, revision, o.done)
}
func (o *X11) Close() error {
	o.once.Do(func() {
		o.mu.Lock()
		o.closing = true
		close(o.quit)
		if o.transport != nil {
			_ = o.transport.raw.Close()
		}
		o.mu.Unlock()
	})
	<-o.done
	return nil
}
func (o *X11) run(t *xTransport) {
	defer func() {
		if t != nil {
			t.close()
		}
		close(o.done)
	}()
	ticker := time.NewTicker(frameInterval)
	defer ticker.Stop()
	start := time.Now()
	var retry, geometryAt time.Time
	scene := Scene{Levels: make([]float64, waveCols)}
	var level float64
	var seen uint64
	var img, scaled *image.RGBA
	for {
		select {
		case <-o.quit:
			return
		case <-o.wake:
		case <-ticker.C:
		}
		if t == nil {
			if time.Now().Before(retry) {
				continue
			}
			fresh, err := connectX11(o.margin, o.fraction)
			if err != nil {
				o.log.Warn("overlay: GNOME HUD unavailable", "error", err)
				retry = time.Now().Add(rebuildRetry)
				continue
			}
			o.mu.Lock()
			if o.closing {
				o.mu.Unlock()
				fresh.close()
				return
			}
			o.transport = fresh
			o.mu.Unlock()
			t = fresh
			o.log.Info("overlay: GNOME HUD rebuilt", "window", uint32(t.win), "scale", t.scale, "screen", t.rect)
			geometryAt = time.Time{}
		}
		select {
		case <-t.lost:
			o.frames.lost(errors.New("overlay: XWayland connection lost"))
			t.close()
			t = nil
			retry = time.Now().Add(rebuildRetry)
			continue
		default:
		}
		// Polling the public layout coalesces changes without an unbounded event
		// queue. Every ambiguity closes the old window instead of guessing placement.
		if time.Since(geometryAt) >= rebuildRetry {
			_ = t.raw.SetDeadline(time.Now().Add(5 * time.Second))
			area, err := t.workarea()
			_ = t.raw.SetDeadline(time.Time{})
			if err == nil {
				var rect image.Rectangle
				var canvas image.Point
				rect, canvas, _, err = hudGeometry(area, o.margin, o.fraction)
				if err == nil && (canvas != t.canvas || area.Scale != t.scale) {
					err = errors.New("overlay: monitor scale changed")
				}
				if err == nil && rect != t.rect {
					err = errors.New("overlay: monitor geometry changed")
				}
			}
			geometryAt = time.Now()
			if err != nil {
				o.frames.lost(err)
				t.close()
				t = nil
				retry = time.Now().Add(rebuildRetry)
				continue
			}
		}
		o.mu.Lock()
		d := o.want
		rev := o.revision
		o.want.levels = nil
		o.mu.Unlock()
		changed := rev != seen
		if changed {
			if scene.Visual != d.visual {
				resetWave(scene.Levels)
				level = 0
			}
			scene.Visual = d.visual
			scene.Preview = d.preview
			if n := len(d.levels); n > 0 {
				level = d.levels[n-1]
				for _, v := range d.levels {
					level = max(level, v)
				}
			}
			seen = rev
		}
		if scene.Visual == Recording {
			shiftWave(scene.Levels, waveDisplayLevel(level))
		}
		if scene.Visual != Hidden || changed || !t.mapped {
			cycle := time.Since(start).Seconds() / pulsePeriod.Seconds()
			scene.Phase = cycle - float64(int(cycle))
			if int(cycle)%2 == 1 {
				scene.Phase = 1 - scene.Phase
			}
			scene.MaxPreviewWidth = t.maxPreview
			scene.SurfaceW = t.canvas.X
			scene.SurfaceH = t.canvas.Y
			if img == nil || img.Bounds().Dx() != scene.SurfaceW || img.Bounds().Dy() != scene.SurfaceH {
				img = image.NewRGBA(image.Rect(0, 0, scene.SurfaceW, scene.SurfaceH))
			}
			err := RenderInto(img, scene)
			if err == nil {
				upload := img
				if t.scale != 1 {
					if scaled == nil || scaled.Bounds().Dx() != t.rect.Dx() || scaled.Bounds().Dy() != t.rect.Dy() {
						scaled = image.NewRGBA(image.Rect(0, 0, t.rect.Dx(), t.rect.Dy()))
					}
					upload = scaled
					draw.BiLinear.Scale(upload, upload.Bounds(), img, img.Bounds(), draw.Src, nil)
				}
				err = t.submit(upload)
			}
			if err != nil {
				o.frames.lost(err)
				o.log.Warn("overlay: GNOME submission lost", "error", err)
				t.close()
				t = nil
				retry = time.Now().Add(rebuildRetry)
				continue
			}
			o.frames.publish(FrameReceipt{Backend: "x11", Revision: rev, Scene: scene, Canvas: img.Bounds().Size(), Screen: t.rect, Scale: t.scale})
		}
		interval := frameInterval
		if scene.Visual == Hidden {
			interval = idleInterval
		}
		ticker.Reset(interval)
	}
}

func (o *X11) RecentFrames() []FrameReceipt { return o.frames.recent() }
