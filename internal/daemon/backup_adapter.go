package daemon

import (
	"context"
	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"strings"
	"time"
)

// startBackup replaces companion preview ownership; it never adds a PCM reader.
func (d *Daemon) startBackup(ctx context.Context, gen uint64) {
	if d.backup != nil {
		_ = d.backup.Cancel(context.Background())
		d.backup = nil
	}
	b, err := NewBackupCycle(ctx, d.companion, BackupCycleOptions{CycleID: d.cycleID, Model: d.companionModel, OnPartial: func(text string) {
		if d.previewEnabled {
			d.setPreview(ctx, gen, soften(text))
		}
	}})
	if err != nil {
		d.logger.Warn("preview: companion start failed — backup unavailable", "err", err)
		return
	}
	d.backup = b
}
func (d *Daemon) feedBackup(pcm []byte) {
	if d.backup == nil {
		return
	}
	if err := d.backup.Feed(pcm); err != nil {
		phase := "feed"
		if strings.Contains(err.Error(), "queue") {
			phase = "queue"
		}
		d.logger.Warn("preview: companion "+phase+" failed — backup unavailable", "err", err)
	}
}
func (d *Daemon) runBackupPump(ctx context.Context) {
	done := d.streamFeedDone
	cr, ok := d.recorder.(audio.ChunkReader)
	if !ok {
		if d.backup != nil {
			_ = d.backup.Cancel(context.Background())
		}
		close(done)
		return
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(PreviewTick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pcm, err := cr.ReadChunk()
				if err != nil {
					if d.backup != nil {
						_ = d.backup.Cancel(context.Background())
					}
					continue
				}
				if len(pcm) > 0 {
					d.feedBackup(pcm)
				}
			}
		}
	}()
}
func (d *Daemon) statusProvenance() (string, string) {
	d.provenanceMu.Lock()
	defer d.provenanceMu.Unlock()
	return d.source, d.warning
}
func (d *Daemon) retainWarning(ctx context.Context) {
	d.provenanceMu.Lock()
	gen := d.warningGen
	d.warningUntil = time.Now().Add(3 * time.Second)
	d.provenanceMu.Unlock()
	d.warningWG.Add(1)
	go func() {
		defer d.warningWG.Done()
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if ctx.Err() != nil {
			return
		}
		d.provenanceMu.Lock()
		defer d.provenanceMu.Unlock()
		if d.warningGen == gen && d.machine.State().String() == "idle" {
			_ = d.overlay.Show(overlay.Hidden)
		}
	}()
}

// usableBackup rejects incomplete, stale-cycle, and cleaned-empty final results.
func usableBackup(r BackupResult, cycleID uint64) bool {
	return r.Complete && r.CycleID == cycleID && output.CleanText(speech.StripNonSpeech(r.Text)) != ""
}
