package desktop

// Controlled request failure with a real mounted companion. The dispatcher is
// observed in memory: this fixture must not type into the focused desktop.
import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/daemon"
	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

func GPUFailureChild() {
	if os.Getenv("MAVOR_DESKTOP_FAILURE_CHILD") != "1" {
		return
	}
	l, e := net.Listen("tcp", os.Getenv("MAVOR_DESKTOP_FAILURE_ADDRESS"))
	if e != nil {
		os.Exit(2)
	}
	_ = http.Serve(l, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "controlled desktop request failure (not hardware OOM)", 500)
	}))
	os.Exit(0)
}

type BackupEvidence struct {
	Text          string `json:"text"`
	FixtureSHA256 string `json:"fixture_sha256"`
	Source        string `json:"source"`
	Model         string `json:"model"`
	Warning       string `json:"warning"`
	Outputs       int    `json:"outputs"`
	History       int    `json:"history"`
	Cycle         uint64 `json:"cycle"`
}

// RunBackup leaves the daemon's warning HUD visible across its idle transition.
// Caller captures it before invoking the returned owned cleanup function.
func RunBackup(o overlay.Overlay) (result BackupEvidence, close func() error, err error) {
	dir, e := os.MkdirTemp("", "mavor-desktop-backup-")
	if e != nil {
		return result, nil, e
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.Default()
	cfg.Model = "zipformer-streaming-20m"
	cfg.Paths.Models = os.Getenv("MAVOR_READINESS_MODEL_DIR")
	cfg.Advanced.GPU = "off"
	cfg.Advanced.Threads = 2
	companion, e := speech.NewSherpaTranscriber(cfg, logger)
	if e != nil {
		os.RemoveAll(dir)
		return result, nil, e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	if e = companion.Start(ctx); e == nil {
		e = speech.VerifyReadiness(ctx, companion)
	}
	if e != nil {
		cancel()
		companion.Close()
		os.RemoveAll(dir)
		return result, nil, e
	}
	source, e := os.ReadFile("../../test/fixtures/real_speech.wav")
	if e != nil {
		cancel()
		companion.Close()
		os.RemoveAll(dir)
		return result, nil, e
	}
	result.FixtureSHA256 = fmt.Sprintf("%x", sha256.Sum256(source))
	wav := filepath.Join(dir, "recording.wav")
	if e = os.WriteFile(wav, source, 0600); e != nil {
		cancel()
		companion.Close()
		os.RemoveAll(dir)
		return result, nil, e
	}
	main := speech.NewServerTranscriber("")
	main.Logger = logger
	main.Supervisor = speech.NewSupervisor(speech.SupervisorConfig{Logger: logger, ReadyTimeout: time.Second, PollInterval: time.Millisecond, CommandFunc: func(ctx context.Context, c speech.SupervisorConfig) *exec.Cmd {
		u, _ := url.Parse(c.ServerSocket)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDesktopGPUFailureChild$")
		cmd.Env = append(os.Environ(), "MAVOR_DESKTOP_FAILURE_CHILD=1", "MAVOR_DESKTOP_FAILURE_ADDRESS="+u.Host)
		return cmd
	}})
	socket := filepath.Join(dir, "mavor.sock")
	out := &output.Mock{}
	store := &history.Store{Path: filepath.Join(dir, "history.jsonl")}
	d := daemon.New(daemon.Config{Socket: socket, Logger: logger, Overlay: retainedHUD{o}, Recorder: &audio.MockRecorder{FixturePath: wav}, Transcriber: main, Output: out, History: store, PreviewMode: speech.PreviewCompanion, PreviewCompanion: companion, ErrorDuration: 50 * time.Millisecond, Initialize: func(context.Context) (daemon.Initialized, error) {
		return daemon.Initialized{Transcriber: main, PreviewMode: speech.PreviewCompanion, PreviewCompanion: companion, MainModel: "controlled-GPU-enabled-server", CompanionModel: cfg.Model}, nil
	}})
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	close = func() error {
		cancel()
		e := <-done
		os.RemoveAll(dir)
		if main.Supervisor.IsRunning() {
			return fmt.Errorf("child still running")
		}
		return e
	}
	failure := func(e error) (BackupEvidence, func() error, error) { _ = close(); return result, nil, e }
	await := func(want string) (ipc.Response, error) {
		deadline := time.Now().Add(30 * time.Second)
		for {
			r, e := ipc.Send(socket, ipc.Request{Action: "status"}, time.Second)
			if e == nil && r.State == want {
				return r, nil
			}
			if time.Now().After(deadline) {
				return r, fmt.Errorf("await %s: %v %+v", want, e, r)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	if _, e = await("idle"); e != nil {
		return failure(e)
	}
	for _, action := range []string{"start", "stop"} {
		if _, e = ipc.Send(socket, ipc.Request{Action: action}, time.Second); e != nil {
			return failure(e)
		}
	}
	status, e := await("idle")
	if e != nil {
		return failure(e)
	}
	entries, e := store.Recent(0)
	if e != nil {
		return failure(e)
	}
	calls := out.Calls()
	if len(calls) != 1 || len(entries) != 1 || entries[0].Source != "companion-backup" || entries[0].Text != calls[0] || status.Warning == "" {
		return failure(fmt.Errorf("backup dispatch mismatch: %v %+v %+v", calls, entries, status))
	}
	result.Text = calls[0]
	result.Source = status.Source
	result.Model = entries[0].Model
	result.Warning = status.Warning
	result.Outputs = len(calls)
	result.History = len(entries)
	result.Cycle = 1
	return result, close, nil
}
