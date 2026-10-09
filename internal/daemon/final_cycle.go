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

// stackingAllowed reports whether a new recording may start while an earlier
// cycle is still transcribing. It may not for the live-final modes (streaming
// and segments), which hold the one main recognizer across the whole capture
// and keep today's behavior (§8, ruling OQ-3); after-stop stacks.
func (d *Daemon) stackingAllowed() bool { return !d.incrementalFinal() }

func (d *Daemon) startFinalCycle(ctx context.Context, cycle *dictationCycle) error {
	if main, ok := d.transcriber.(speech.StreamTranscriber); ok && d.companion == main && d.previewMode == speech.PreviewCompanion {
		return errors.New("main final and companion cannot share a recognizer")
	}
	d.awaitStreamDrain(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	d.streamMu.Lock()
	d.liveID = cycle.id
	cycle.hadSpeech = false
	d.streamHistory = ""
	d.streamMu.Unlock()
	c := &incrementalCycle{done: make(chan struct{})}
	onPartial := func(text string) {
		// Recognition evidence is independent of whether the HUD is enabled.
		if speech.StripNonSpeech(text) != "" {
			d.streamMu.Lock()
			if d.liveID == cycle.id && ctx.Err() == nil {
				cycle.hadSpeech = true
			}
			d.streamMu.Unlock()
		}
		if d.previewEnabled && d.previewMode != speech.PreviewCompanion {
			d.setPreview(ctx, cycle, soften(text))
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
		d.startBackup(ctx, cycle)
	}
	cr, ok := d.recorder.(audio.ChunkReader)
	if !ok {
		session.FailLive("recorder does not implement ChunkReader")
		d.cancelBackup()
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
					d.cancelBackup()
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
		d.liveID = 0
		if d.overlay != nil {
			_ = d.overlay.SetText("")
		}
		d.streamMu.Unlock()
	})
}
func (d *Daemon) cancelFinalCycle() {
	d.stopFinalPump()
	d.cancelBackup()
	if d.finalCycle != nil {
		_ = d.finalCycle.session.Cancel(context.Background())
	}
}
