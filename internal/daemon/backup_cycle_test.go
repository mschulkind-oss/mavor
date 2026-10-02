package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

func backupWAV(t *testing.T, pcm []byte) string {
	t.Helper()
	b := make([]byte, 44+len(pcm))
	copy(b, "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(len(b)-8))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], 16000)
	binary.LittleEndian.PutUint32(b[28:], 32000)
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	copy(b[44:], pcm)
	p := filepath.Join(t.TempDir(), "final.wav")
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return p
}
func TestBackupFinalTailAndTerminalTokens(t *testing.T) {
	s := speech.NewMockStreamTranscriber("hello final word", "hello")
	c, err := NewBackupCycle(context.Background(), s, BackupCycleOptions{CycleID: 7, Model: "companion"})
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Feed([]byte{1, 0}); err != nil {
		t.Fatal(err)
	}
	r, err := c.Finish(context.Background(), backupWAV(t, []byte{1, 0, 2, 0, 0, 0}))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Complete || r.CycleID != 7 || r.Model != "companion" || r.Text != "hello final word" || r.Samples != 3 {
		t.Fatalf("%+v", r)
	}
	var got []byte
	for _, p := range s.Chunks() {
		got = append(got, p...)
	}
	if string(got) != string([]byte{1, 0, 2, 0, 0, 0}) {
		t.Fatalf("coverage %v", got)
	}
	if c.Feed([]byte{0, 0}) == nil {
		t.Fatal("late feed accepted")
	}
	if _, err = c.Finish(context.Background(), ""); err == nil {
		t.Fatal("second finish accepted")
	}
}
func TestBackupRefusesMissingOrCorruptCoverage(t *testing.T) {
	for _, tc := range []struct {
		name        string
		live, final []byte
	}{{"mismatch", []byte{1, 0}, []byte{2, 0}}, {"overlong", []byte{1, 0, 2, 0}, []byte{1, 0}}, {"odd", []byte{1}, []byte{1, 0}}, {"overflow", make([]byte, 768002), []byte{0, 0}}} {
		t.Run(tc.name, func(t *testing.T) {
			s := speech.NewMockStreamTranscriber("must not escape")
			c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{})
			if e != nil {
				t.Fatal(e)
			}
			_ = c.Feed(tc.live)
			r, e := c.Finish(context.Background(), backupWAV(t, tc.final))
			if e == nil || r.Complete || r.Text != "" {
				t.Fatalf("%+v %v", r, e)
			}
			if s.IsStreaming() {
				t.Fatal("stream left active")
			}
		})
	}
}
func TestBackupEmptyCleanupAndCancel(t *testing.T) {
	s := speech.NewMockStreamTranscriber("[BLANK_AUDIO]")
	c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{})
	if e != nil {
		t.Fatal(e)
	}
	r, e := c.Finish(context.Background(), backupWAV(t, []byte{0, 0}))
	if e != nil || !r.Complete || r.Text != "" {
		t.Fatalf("%+v %v", r, e)
	}
	c, e = NewBackupCycle(context.Background(), s, BackupCycleOptions{CycleID: 2})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Cancel(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = c.Cancel(context.Background()); e != nil {
		t.Fatal(e)
	}
	r, e = c.Finish(context.Background(), backupWAV(t, []byte{0, 0}))
	if e == nil || r.Complete {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestBackupDeadlineJoinsFeed(t *testing.T) {
	s := speech.NewMockStreamTranscriber("stale")
	entered := make(chan struct{})
	s.SetFeedFn(func(ctx context.Context, _ []byte) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	})
	c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{FinalizeTimeout: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	_ = c.Feed([]byte{1, 0})
	<-entered
	r, e := c.Finish(context.Background(), backupWAV(t, []byte{1, 0}))
	if e == nil || r.Complete || s.IsStreaming() {
		t.Fatalf("%+v %v", r, e)
	}
}

func TestBackupFailuresNeverReturnProvisionalText(t *testing.T) {
	for _, phase := range []string{"start", "feed", "stop"} {
		t.Run(phase, func(t *testing.T) {
			s := speech.NewMockStreamTranscriber("final", "provisional")
			boom := errors.New("production diagnostic")
			switch phase {
			case "start":
				s.SetErrors(boom, nil, nil)
			case "feed":
				s.SetErrors(nil, boom, nil)
			case "stop":
				s.SetErrors(nil, nil, boom)
			}
			c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{})
			if phase == "start" {
				if !errors.Is(e, boom) || s.IsStreaming() {
					t.Fatalf("%v", e)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			_ = c.Feed([]byte{1, 0})
			r, e := c.Finish(context.Background(), backupWAV(t, []byte{1, 0}))
			if !errors.Is(e, boom) || r.Text != "" || r.Complete || s.IsStreaming() {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
}

type backupSerialized struct {
	speech.Mock
	active      atomic.Int32
	overlap     atomic.Bool
	stopEntered chan struct{}
	release     chan struct{}
}

func (s *backupSerialized) enter() func() {
	if s.active.Add(1) != 1 {
		s.overlap.Store(true)
	}
	return func() { s.active.Add(-1) }
}
func (s *backupSerialized) StartStream(context.Context) error { defer s.enter()(); return nil }
func (s *backupSerialized) FeedChunk(context.Context, []byte) (string, error) {
	defer s.enter()()
	return "provisional", nil
}
func (s *backupSerialized) StopStream(ctx context.Context) (string, error) {
	defer s.enter()()
	close(s.stopEntered)
	<-s.release
	return "old cycle", nil
}
func (s *backupSerialized) AbortStream(context.Context) error { defer s.enter()(); return nil }

func TestBackupCancelConcurrentFinishJoinsBeforeReuse(t *testing.T) {
	s := &backupSerialized{stopEntered: make(chan struct{}), release: make(chan struct{})}
	c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{CycleID: 10})
	if e != nil {
		t.Fatal(e)
	}
	path := backupWAV(t, []byte{0, 0})
	finished := make(chan BackupResult, 1)
	go func() { r, _ := c.Finish(context.Background(), path); finished <- r }()
	<-s.stopEntered
	canceled := make(chan struct{})
	go func() { _ = c.Cancel(context.Background()); close(canceled) }()
	// Wait until cancellation is published, then prove Cancel cannot return while
	// an uncancellable native StopStream call is still running.
	<-c.ctx.Done()
	select {
	case <-canceled:
		t.Fatal("cancel abandoned native call")
	default:
	}
	close(s.release)
	<-canceled
	r := <-finished
	if r.Complete || r.Text != "" {
		t.Fatalf("stale result %+v", r)
	}
	if s.overlap.Load() {
		t.Fatal("concurrent recognizer access")
	}
	s.stopEntered = make(chan struct{})
	s.release = make(chan struct{})
	close(s.release)
	next, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{CycleID: 11})
	if e != nil {
		t.Fatal(e)
	}
	r, e = next.Finish(context.Background(), path)
	if e != nil || r.CycleID != 11 || !r.Complete {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestBackupPartialIsNotFinalAuthority(t *testing.T) {
	s := speech.NewMockStreamTranscriber("", "preview only")
	partial := make(chan string, 1)
	c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{OnPartial: func(text string) { partial <- text }})
	if e != nil {
		t.Fatal(e)
	}
	_ = c.Feed([]byte{1, 0})
	if text := <-partial; text != "preview only" {
		t.Fatal(text)
	}
	r, e := c.Finish(context.Background(), backupWAV(t, []byte{1, 0}))
	if e != nil || r.Text != "" || !r.Complete {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestBackupRejectsInvalidWAV(t *testing.T) {
	for _, kind := range []string{"rate", "channels", "bits", "truncated", "missing"} {
		t.Run(kind, func(t *testing.T) {
			p := backupWAV(t, []byte{0, 0})
			b, e := os.ReadFile(p)
			if e != nil {
				t.Fatal(e)
			}
			switch kind {
			case "rate":
				binary.LittleEndian.PutUint32(b[24:], 8000)
			case "channels":
				binary.LittleEndian.PutUint16(b[22:], 2)
			case "bits":
				binary.LittleEndian.PutUint16(b[34:], 8)
			case "truncated":
				b = b[:len(b)-1]
			case "missing":
				copy(b[36:], "JUNK")
			}
			if e = os.WriteFile(p, b, 0600); e != nil {
				t.Fatal(e)
			}
			s := speech.NewMockStreamTranscriber("forbidden")
			c, e := NewBackupCycle(context.Background(), s, BackupCycleOptions{})
			if e != nil {
				t.Fatal(e)
			}
			r, e := c.Finish(context.Background(), p)
			if e == nil || r.Complete || s.IsStreaming() {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
}

// Opt-in real CPU companion acceptance: local mounted model and checked-in
// speech fixture only; no microphone, downloads, output, history or desktop.
func TestBackupMountedCompanionSmoke(t *testing.T) {
	dir := os.Getenv("MAVOR_BACKUP_SMOKE_MODEL_DIR")
	if dir == "" {
		t.Skip("set MAVOR_BACKUP_SMOKE_MODEL_DIR to an installed model cache")
	}
	cfg := config.Default()
	cfg.Model = "zipformer-streaming-20m"
	cfg.Paths.Models = dir
	src, e := speech.NewSherpaTranscriber(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if e != nil {
		t.Fatal(e)
	}
	defer src.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if e = src.Start(ctx); e != nil {
		t.Fatal(e)
	}
	path, e := filepath.Abs("../../test/fixtures/real_speech.wav")
	if e != nil {
		t.Fatal(e)
	}
	empty := sha256.Sum256(nil)
	f, total, e := backupPCM(path, 0, empty[:], ctx)
	if e != nil {
		t.Fatal(e)
	}
	pcm := make([]byte, total)
	_, e = io.ReadFull(f, pcm)
	_ = f.Close()
	if e != nil {
		t.Fatal(e)
	}
	// Run twice to verify final drain and native stream reuse, including terminal
	// text and a quiet tail recovered from final WAV instead of the live feed.
	for id := uint64(1); id <= 2; id++ {
		c, e := NewBackupCycle(ctx, src, BackupCycleOptions{CycleID: id, Model: cfg.Model})
		if e != nil {
			t.Fatal(e)
		}
		live := len(pcm) - 32000
		if live < 0 {
			live = 0
		}
		for pos := 0; pos < live; {
			end := pos + 3200
			if end > live {
				end = live
			}
			if e = c.Feed(pcm[pos:end]); e != nil {
				_ = c.Cancel(context.Background())
				t.Fatal(e)
			}
			pos = end
		}
		r, e := c.Finish(ctx, path)
		if e != nil || !r.Complete || r.Text == "" || r.CycleID != id || r.Samples != total/2 {
			t.Fatalf("%+v %v", r, e)
		}
		words := strings.Fields(strings.ToLower(strings.TrimRight(r.Text, ".")))
		if words[len(words)-1] == "the" {
			t.Fatalf("terminal word dropped: %q", r.Text)
		}
		t.Logf("cycle %d, samples %d, finalized companion: %q", id, r.Samples, r.Text)
	}
}
