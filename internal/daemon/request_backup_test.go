package daemon

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type backupHistory struct{ entries []history.Entry }

func (h *backupHistory) Append(e history.Entry) error { h.entries = append(h.entries, e); return nil }
func TestRequestFailureSelectsFinalizedBackupOnce(t *testing.T) {
	for _, tc := range []struct {
		name, main, backup string
		err                error
		want               string
	}{{"gpu", "", "final backup", &speech.GPURequestError{Err: errors.New("server status 500")}, "final backup"}, {"cpu", "", "final backup", errors.New("cpu failed"), ""}, {"success", "authoritative", "final backup", nil, "authoritative"}, {"empty success", "", "final backup", nil, ""}, {"blank backup", "", "[BLANK_AUDIO]", &speech.GPURequestError{Err: errors.New("500")}, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			wav := backupWAV(t, []byte{1, 0, 2, 0})
			h := &backupHistory{}
			out := &output.Mock{}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Recorder = &audio.MockRecorder{FixturePath: wav}
				c.Transcriber = &speech.Mock{Text: tc.main, Err: tc.err}
				c.PreviewMode = speech.PreviewCompanion
				c.PreviewCompanion = speech.NewMockStreamTranscriber(tc.backup, "provisional")
				c.Output = out
				c.History = h
			})
			stop := runDaemon(t, d)
			sendWithRetry(t, sock, "start")
			sendWithRetry(t, sock, "stop")
			waitForState(t, sock, "idle")
			status := sendWithRetry(t, sock, "status")
			stop()
			got := out.Calls()
			if tc.want == "" {
				if len(got) != 0 || len(h.entries) != 0 {
					t.Fatalf("unexpected dispatch %+v %+v", got, h.entries)
				}
				return
			}
			if len(got) != 1 || got[0] != tc.want || len(h.entries) != 1 {
				t.Fatalf("dispatch %+v %+v", got, h.entries)
			}
			if tc.name == "gpu" && (h.entries[0].Source != "companion-backup" || status.Warning == "" || status.Source != "companion-backup") {
				t.Fatalf("missing provenance %+v %+v", h.entries, status)
			}
		})
	}
}

func TestIncompleteOrMissingBackupRetainsOriginalFailure(t *testing.T) {
	for _, failure := range []string{"missing", "mismatch", "odd", "cancelled", "deadline"} {
		t.Run(failure, func(t *testing.T) {
			wav := backupWAV(t, []byte{1, 0, 2, 0})
			rec := &audio.MockRecorder{FixturePath: wav}
			if failure == "mismatch" {
				rec.SetChunks([]byte{99, 0})
			}
			if failure == "odd" {
				rec.SetChunks([]byte{1})
			}
			original := error(&speech.GPURequestError{Err: errors.New("original GPU-enabled request diagnostic")})
			if failure == "cancelled" {
				original = &speech.GPURequestError{Err: context.Canceled}
			}
			if failure == "deadline" {
				original = &speech.GPURequestError{Err: context.DeadlineExceeded}
			}
			out := &output.Mock{}
			store := &history.Store{Path: t.TempDir() + "/history.jsonl"}
			d, sock := newTestDaemon(t, func(c *Config) {
				c.Recorder = rec
				c.Output = out
				c.History = store
				c.Transcriber = &speech.Mock{Text: "partial main must not emit", Err: original}
				c.PreviewMode = speech.PreviewCompanion
				if failure != "missing" {
					c.PreviewCompanion = speech.NewMockStreamTranscriber("backup")
				}
			})
			stop := runDaemon(t, d)
			defer stop()
			sendWithRetry(t, sock, "start")
			if failure == "mismatch" || failure == "odd" {
				time.Sleep(2 * PreviewTick)
			}
			sendWithRetry(t, sock, "stop")
			waitForState(t, sock, "idle")
			status := sendWithRetry(t, sock, "status")
			if got := out.Calls(); len(got) != 0 {
				t.Fatal(got)
			}
			entries, e := store.Recent(0)
			if e != nil || len(entries) != 0 {
				t.Fatalf("%+v %v", entries, e)
			}
			if status.Warning != original.Error() {
				t.Fatalf("lost original diagnostic: %+v", status)
			}
		})
	}
}

func TestBackupSelectionRejectsStaleAndIncomplete(t *testing.T) {
	for _, r := range []BackupResult{{CycleID: 1, Text: "final", Complete: true}, {CycleID: 2, Text: "final", Complete: false}, {CycleID: 2, Text: "[BLANK_AUDIO]", Complete: true}} {
		if usableBackup(r, 2) {
			t.Fatalf("unsafe result: %+v", r)
		}
	}
	if !usableBackup(BackupResult{CycleID: 2, Text: "final", Complete: true}, 2) {
		t.Fatal("same-cycle final refused")
	}
}
