package daemon

import (
	"context"
	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDaemonGPUHelperProcess(t *testing.T) {
	if os.Getenv("MAVOR_DAEMON_GPU_HELPER") == "" {
		return
	}
	listener, e := net.Listen("tcp", os.Getenv("MAVOR_DAEMON_GPU_ADDRESS"))
	if e != nil {
		os.Exit(2)
	}
	var requests atomic.Int32
	_ = http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if os.Getenv("MAVOR_DAEMON_GPU_CHUNK2") == "1" && requests.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"text":"partial main success must not emit"}`)
			return
		}
		http.Error(w, "controlled runtime allocation failure", 500)
	}))
	os.Exit(0)
}

type diagnosticHUD struct {
	overlay.Noop
	mu          sync.Mutex
	visuals     []overlay.Visual
	times       []time.Time
	text        string
	visual      overlay.Visual
	diagnostics map[overlay.Visual]string
}

func (h *diagnosticHUD) Show(v overlay.Visual) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if v != h.visual {
		h.text = ""
	}
	h.visual = v
	h.visuals = append(h.visuals, v)
	h.times = append(h.times, time.Now())
	return nil
}
func (h *diagnosticHUD) SetText(s string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Match production clear-on-transition and X11 subtitle gating.
	if h.visual == overlay.Recording || h.visual == overlay.Initializing || h.visual == overlay.Error || h.visual == overlay.Degraded {
		h.text = s
		if h.diagnostics == nil {
			h.diagnostics = make(map[overlay.Visual]string)
		}
		h.diagnostics[h.visual] = s
	}
	return nil
}
func TestActualSupervisedGPURequestWarnsThenDispatchesBackup(t *testing.T) {
	wav := backupWAV(t, []byte{1, 0, 2, 0})
	store := &history.Store{Path: t.TempDir() + "/history.jsonl"}
	out := &output.Mock{}
	hud := &diagnosticHUD{}
	st := speech.NewServerTranscriber("")
	st.Supervisor = speech.NewSupervisor(speech.SupervisorConfig{ReadyTimeout: time.Second, PollInterval: time.Millisecond, CommandFunc: func(ctx context.Context, c speech.SupervisorConfig) *exec.Cmd {
		u, _ := url.Parse(c.ServerSocket)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonGPUHelperProcess$")
		cmd.Env = append(os.Environ(), "MAVOR_DAEMON_GPU_HELPER=1", "MAVOR_DAEMON_GPU_ADDRESS="+u.Host)
		return cmd
	}})
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: wav}
		c.Transcriber = st
		c.Output = out
		c.History = store
		c.Overlay = hud
		c.ErrorDuration = 30 * time.Millisecond
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = speech.NewMockStreamTranscriber("safe finalized backup", "never type provisional")
	})
	d.companionModel = "controlled-companion"
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	status := sendWithRetry(t, sock, "status")
	if got := out.Calls(); len(got) != 1 || got[0] != "safe finalized backup" {
		t.Fatal(got)
	}
	entries, e := store.Recent(0)
	if e != nil || len(entries) != 1 || entries[0].Source != "companion-backup" || entries[0].Model != "controlled-companion" || !strings.Contains(entries[0].Warning, "controlled runtime allocation failure") {
		t.Fatalf("%+v %v", entries, e)
	}
	hud.mu.Lock()
	var errorAt time.Time
	var saw bool
	for i, v := range hud.visuals {
		if v == overlay.Error {
			errorAt = hud.times[i]
		}
		if v == overlay.Degraded {
			if errorAt.IsZero() || hud.times[i].Sub(errorAt) < 30*time.Millisecond {
				t.Error("warning not retained before backup")
			}
			saw = true
		}
	}
	if !saw || !strings.Contains(hud.text, "controlled runtime allocation failure") {
		t.Errorf("warning missing: %v %s", hud.visuals, hud.text)
	}
	hud.mu.Unlock()
	if status.Source != "companion-backup" || status.Warning == "" {
		t.Fatal(status)
	}
}

func TestSecondGPUChunkFailureDispatchesOnlyWholeBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "long.wav")
	if e := audio.WriteWAV(path, make([]int16, 40*16000), 16000); e != nil {
		t.Fatal(e)
	}
	st := speech.NewServerTranscriber("")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st.Logger = logger
	st.Supervisor = speech.NewSupervisor(speech.SupervisorConfig{Logger: logger, ReadyTimeout: time.Second, PollInterval: time.Millisecond, CommandFunc: func(ctx context.Context, c speech.SupervisorConfig) *exec.Cmd {
		u, _ := url.Parse(c.ServerSocket)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonGPUHelperProcess$")
		cmd.Env = append(os.Environ(), "MAVOR_DAEMON_GPU_HELPER=1", "MAVOR_DAEMON_GPU_CHUNK2=1", "MAVOR_DAEMON_GPU_ADDRESS="+u.Host)
		return cmd
	}})
	out := &output.Mock{}
	store := &history.Store{Path: filepath.Join(t.TempDir(), "history.jsonl")}
	d, sock := newTestDaemon(t, func(c *Config) {
		c.Recorder = &audio.MockRecorder{FixturePath: path}
		c.Transcriber = speech.WrapChunking(st, "overlap", logger)
		c.Output = out
		c.History = store
		c.PreviewMode = speech.PreviewCompanion
		c.PreviewCompanion = speech.NewMockStreamTranscriber("whole finalized backup")
	})
	stop := runDaemon(t, d)
	defer stop()
	sendWithRetry(t, sock, "start")
	sendWithRetry(t, sock, "stop")
	waitForState(t, sock, "idle")
	if got := out.Calls(); len(got) != 1 || got[0] != "whole finalized backup" {
		t.Fatalf("double/partial dispatch: %v", got)
	}
	entries, e := store.Recent(0)
	if e != nil || len(entries) != 1 || entries[0].Text != "whole finalized backup" || entries[0].Source != "companion-backup" {
		t.Fatalf("%+v %v", entries, e)
	}
}
