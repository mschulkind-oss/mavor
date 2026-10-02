package daemon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type incrementalCycle struct {
	session         speech.FinalSession
	cancel          context.CancelFunc
	done            chan struct{}
	once            sync.Once
	companionQueue  chan []byte
	companionCancel context.CancelFunc
	companionWarn   sync.Once
}

func (d *Daemon) incrementalFinal() bool {
	return d.finalMode != "" && d.finalMode != models.FinalAfterStop
}
func (d *Daemon) startFinalCycle(ctx context.Context) error {
	if main, ok := d.transcriber.(speech.StreamTranscriber); ok && d.companion == main && d.previewMode == speech.PreviewCompanion {
		return errors.New("main final and companion cannot share a recognizer")
	}
	d.awaitStreamDrain(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	d.streamMu.Lock()
	d.streamGen++
	gen := d.streamGen
	d.streamHadSpeech = false
	d.streamHistory = ""
	d.streamMu.Unlock()
	c := &incrementalCycle{done: make(chan struct{})}
	onPartial := func(text string) {
		// Recognition evidence is independent of whether the HUD is enabled.
		if speech.StripNonSpeech(text) != "" {
			d.streamMu.Lock()
			if d.streamGen == gen && ctx.Err() == nil {
				d.streamHadSpeech = true
			}
			d.streamMu.Unlock()
		}
		if d.previewEnabled && d.previewMode != speech.PreviewCompanion {
			d.setPreview(ctx, gen, soften(text))
		}
	}
	session, err := speech.NewFinalSession(ctx, d.transcriber, speech.FinalSessionOptions{Mode: d.finalMode, Logger: d.logger, OnPartial: onPartial})
	if err != nil {
		return err
	}
	c.session = session
	pumpCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	d.finalCycle = c
	if d.previewEnabled && d.previewMode == speech.PreviewCompanion && d.companion != nil {
		// The companion has a disposable bounded queue: it can never backpressure
		// the main consumer or own authoritative text. Reuse waits for its drain.
		c.companionQueue = make(chan []byte, 32)
		previewCtx, previewCancel := context.WithCancel(ctx)
		c.companionCancel = previewCancel
		drain := make(chan struct{})
		d.streamMu.Lock()
		d.streamDrain = drain
		d.streamMu.Unlock()
		go func() {
			defer close(drain)
			src := d.companion
			if err := src.StartStream(previewCtx); err != nil {
				d.logger.Warn("preview: companion start failed — dictation is unaffected", "err", err)
				return
			}
			for {
				select {
				case <-previewCtx.Done():
					final, err := src.StopStream(context.Background())
					if err == nil && speech.StripNonSpeech(final) != "" {
						d.streamMu.Lock()
						if d.streamGen == gen+1 {
							d.streamHadSpeech = true
						}
						d.streamMu.Unlock()
					}
					return
				case pcm := <-c.companionQueue:
					text, err := src.FeedChunk(previewCtx, pcm)
					if err == nil {
						d.setPreview(previewCtx, gen, soften(text))
					} else if previewCtx.Err() == nil {
						c.companionWarn.Do(func() { d.logger.Warn("preview: companion feed failed — dictation is unaffected", "err", err) })
					}
				}
			}
		}()
	}
	cr, ok := d.recorder.(audio.ChunkReader)
	if !ok {
		session.FailLive("recorder does not implement ChunkReader")
		close(c.done)
		return nil
	}
	go func() {
		defer close(c.done)
		ticker := time.NewTicker(PreviewTick)
		defer ticker.Stop()
		for {
			select {
			case <-pumpCtx.Done():
				return
			case <-ticker.C:
				pcm, err := cr.ReadChunk()
				if err != nil {
					session.FailLive("ReadChunk: " + err.Error())
					continue
				}
				if len(pcm) == 0 {
					continue
				}
				_ = session.Feed(pcm)
				if c.companionQueue != nil {
					select {
					case c.companionQueue <- append([]byte(nil), pcm...):
					default:
						c.companionWarn.Do(func() {
							d.logger.Warn("preview: companion queue full — dropping preview audio, dictation is unaffected")
						})
					}
				}
			}
		}
	}()
	return nil
}
func (d *Daemon) stopFinalPump() {
	c := d.finalCycle
	if c == nil {
		return
	}
	c.once.Do(func() {
		c.cancel()
		<-c.done
		d.streamMu.Lock()
		d.streamGen++
		if d.overlay != nil {
			_ = d.overlay.SetText("")
		}
		d.streamMu.Unlock()
		if c.companionCancel != nil {
			c.companionCancel()
		}
	})
}
func (d *Daemon) cancelFinalCycle() {
	d.stopFinalPump()
	if d.finalCycle != nil {
		_ = d.finalCycle.session.Cancel(context.Background())
	}
}
