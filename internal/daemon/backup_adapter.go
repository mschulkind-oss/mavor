package daemon

import (
	"context"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// The companion backup is a single-instance resource, like the recorder: one
// stream at a time. These accessors keep its pointer under one lock so the
// control path, the preview pump, the decode worker, and shutdown never race
// the pointer itself.
func (d *Daemon) takeBackup() *BackupCycle {
	d.backupMu.Lock()
	defer d.backupMu.Unlock()
	b := d.backup
	d.backup = nil
	return b
}

func (d *Daemon) installBackup(b *BackupCycle) {
	d.backupMu.Lock()
	d.backup = b
	d.backupMu.Unlock()
}

func (d *Daemon) currentBackup() *BackupCycle {
	d.backupMu.Lock()
	defer d.backupMu.Unlock()
	return d.backup
}

func (d *Daemon) cancelBackup() {
	if b := d.takeBackup(); b != nil {
		_ = b.Cancel(context.Background())
	}
}

// startBackup replaces companion preview ownership; it never adds a PCM reader.
func (d *Daemon) startBackup(ctx context.Context, cycle *dictationCycle) {
	d.cancelBackup()
	b, err := NewBackupCycle(ctx, d.companion, BackupCycleOptions{CycleID: cycle.id, Model: d.companionModel, OnPartial: func(text string) {
		if d.previewEnabled {
			d.setPreview(ctx, cycle, soften(text))
		}
	}})
	if err != nil {
		d.logger.Warn("preview: companion start failed — backup unavailable", "err", err)
		return
	}
	d.installBackup(b)
}
func (d *Daemon) feedBackup(pcm []byte) {
	b := d.currentBackup()
	if b == nil {
		return
	}
	if err := b.Feed(pcm); err != nil {
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
		d.cancelBackup()
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
					d.cancelBackup()
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
