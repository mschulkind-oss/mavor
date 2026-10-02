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
	session speech.FinalSession
	cancel  context.CancelFunc
	done    chan struct{}
	once    sync.Once
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
	if d.previewMode == speech.PreviewCompanion && d.companion != nil {
		d.startBackup(ctx, gen)
	}
	cr, ok := d.recorder.(audio.ChunkReader)
	if !ok {
		session.FailLive("recorder does not implement ChunkReader")
		if d.backup != nil {
			_ = d.backup.Cancel(context.Background())
		}
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
					if d.backup != nil {
						_ = d.backup.Cancel(context.Background())
					}
					continue
				}
				if len(pcm) == 0 {
					continue
				}
				_ = session.Feed(pcm)
				d.feedBackup(pcm)
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
	})
}
func (d *Daemon) cancelFinalCycle() {
	d.stopFinalPump()
	if d.backup != nil {
		_ = d.backup.Cancel(context.Background())
	}
	if d.finalCycle != nil {
		_ = d.finalCycle.session.Cancel(context.Background())
	}
}
