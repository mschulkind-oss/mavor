package daemon

import (
	"context"
	"crypto/sha256"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type observedCompanion struct {
	*speech.SherpaTranscriber
	mu    sync.Mutex
	final string
}

func (c *observedCompanion) StopStream(ctx context.Context) (string, error) {
	text, e := c.SherpaTranscriber.StopStream(ctx)
	c.mu.Lock()
	c.final = text
	c.mu.Unlock()
	return text, e
}

// Real CPU companion, checked-in local speech, controlled supervised HTTP 500.
// This is not evidence of hardware GPU/OOM failure or microphone acceptance.
func TestMountedCompanionRequestBackupEndToEnd(t *testing.T) {
	root := os.Getenv("MAVOR_READINESS_MODEL_DIR")
	if root == "" {
		t.Skip("existing mounted models required")
	}
	cfg := config.Default()
	cfg.Model = "zipformer-streaming-20m"
	cfg.Paths.Models = root
	cfg.Advanced.GPU = "off"
	cfg.Advanced.Threads = 2
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	tr, e := speech.NewSherpaTranscriber(cfg, logger)
	if e != nil {
		t.Fatal(e)
	}
	defer tr.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	if e = tr.Start(ctx); e != nil {
		t.Fatal(e)
	}
	if e = speech.VerifyReadiness(ctx, tr); e != nil {
		t.Fatal(e)
	}
	companion := &observedCompanion{SherpaTranscriber: tr}
	source, e := os.ReadFile("../../test/fixtures/real_speech.wav")
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("controlled speech fixture SHA256=%x bytes=%d", sha256.Sum256(source), len(source))
	wav := filepath.Join(t.TempDir(), "recording.wav")
	if e = os.WriteFile(wav, source, 0600); e != nil {
		t.Fatal(e)
	}
	st := speech.NewServerTranscriber("")
	st.Logger = logger
	st.Supervisor = speech.NewSupervisor(speech.SupervisorConfig{Logger: logger, ReadyTimeout: time.Second, PollInterval: time.Millisecond, CommandFunc: func(ctx context.Context, c speech.SupervisorConfig) *exec.Cmd {
		u, _ := url.Parse(c.ServerSocket)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonGPUHelperProcess$")
		cmd.Env = append(os.Environ(), "MAVOR_DAEMON_GPU_HELPER=1", "MAVOR_DAEMON_GPU_ADDRESS="+u.Host)
		return cmd
	}})
	hud := &diagnosticHUD{}
	out := &output.Mock{}
	store := &history.Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: wav}
		c.Transcriber = st
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = companion
		c.Overlay = hud
		c.Output = out
		c.History = store
		c.ErrorDuration = 30 * time.Millisecond
	})
	d.companionModel = cfg.Model
	stop := runDaemon(t, d)
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	status := sendWithRetry(t, sock, "status")
	stop()
	companion.mu.Lock()
	final := companion.final
	companion.mu.Unlock()
	want := output.CleanText(speech.StripNonSpeech(final))
	got := out.Calls()
	entries, e := store.Recent(0)
	if want == "" || len(got) != 1 || got[0] != want || e != nil || len(entries) != 1 || entries[0].Text != want || entries[0].Source != "companion-backup" || entries[0].Model != cfg.Model || status.Source != "companion-backup" || !strings.Contains(status.Warning, "controlled runtime allocation failure") {
		t.Fatalf("final=%q output=%v history=%+v status=%+v err=%v", final, got, entries, status, e)
	}
	hud.mu.Lock()
	warning := hud.diagnostics[overlay.Degraded]
	diagnostic := hud.diagnostics[overlay.Error]
	hud.mu.Unlock()
	if warning == "" || diagnostic == "" {
		t.Fatal("lost production-equivalent diagnostics")
	}
	if st.Supervisor.IsRunning() {
		t.Fatal("supervised child survived shutdown")
	}
	if _, e = os.Stat(sock); !os.IsNotExist(e) {
		t.Fatal("socket survived shutdown")
	}
	t.Logf("same cycle=%d finalized=%q source=%s model=%s output_count=%d history_count=%d warning=%q child_reaped=true", d.cycleID, final, status.Source, entries[0].Model, len(got), len(entries), warning)
}
