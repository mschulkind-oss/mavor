package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
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

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "setup", "install":
		exit(runSetup(args))
	case "doctor":
		exit(runDoctor(args))
	case "daemon":
		exit(runDaemon(args))
	case "toggle":
		exit(runToggle())
	case "start":
		exit(runStart())
	case "stop":
		exit(runStop())
	case "status":
		exit(runStatus())
	case "config":
		exit(runConfig(args))
	case "service":
		exit(runService(args))
	case "models":
		exit(runModels(args))
	case "logs":
		exit(runLogs(args))
	case "history":
		exit(runHistory(os.Args[2:]))
	case "version", "-v", "--version":
		exit(runVersion())
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n", os.Args[1])
		usage(os.Stderr)
		os.Exit(2)
	}
}

// transcriptStore returns the transcript history log, or nil if its location
// cannot be resolved — losing the recovery log must not stop the daemon.
func transcriptStore(logger *slog.Logger) daemon.TranscriptRecorder {
	store, err := history.New()
	if err != nil {
		logger.Warn("history: disabled (cannot resolve path)", "err", err)
		return nil
	}
	return store
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `mavor — low-latency voice dictation for Wayland

usage: mavor <command> [args]

First-Run & Setup:
  setup [--force]                       one-shot setup (creates config, downloads default model)
  doctor [--fix]                        validate environment or auto-fix missing setup

Core Commands:
  daemon [--verbose] [--log-file PATH]  run the long-lived voice dictation daemon
  toggle                                start recording or stop+transcribe (toggle mode)
  start                                 start voice capture (push-to-talk key press)
  stop                                  stop recording and transcribe (push-to-talk key release)
  status                                print the daemon's current state (idle/recording/transcribing)
  logs [-f|--follow] [-n <lines>]       view or stream real-time daemon logs
  history [-n N] [--json] [--copy]      list past transcripts, newest first, or recover one

Environment & Service Management:
  config [init|show|path]               initialize or inspect ~/.config/mavor/config.toml
  service [install|start|status|stop]   manage systemd user service (~/.config/systemd/user/mavor.service)
  models [pull <name>|list]             download or view cached voice models
  version                               show version and build tags
  help                                  show this help message

Keybinding example (sway; adapt for your compositor):
  exec mavor daemon
  bindsym $mod+grave exec mavor toggle`)
}

func exit(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func runDaemon(args []string) error {
	verbose := false
	logFile := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-v" || a == "--verbose":
			verbose = true
		case a == "--log-file" && i+1 < len(args):
			logFile = args[i+1]
			i++
		case strings.HasPrefix(a, "--log-file="):
			logFile = strings.TrimPrefix(a, "--log-file=")
		}
	}
	// A systemd user service may start before the compositor has exported
	// WAYLAND_DISPLAY into the environment it inherited, so recover it from
	// the socket on disk. The .lock file sits beside the socket and sorts
	// after it, so take the first match rather than any match.
	if os.Getenv("WAYLAND_DISPLAY") == "" {
		if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
			for _, m := range globSorted(filepath.Join(rt, "wayland-*")) {
				if !strings.HasSuffix(m, ".lock") {
					os.Setenv("WAYLAND_DISPLAY", filepath.Base(m))
					break
				}
			}
		}
	}

	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	logLevel := slog.LevelInfo
	if verbose {
		logLevel = slog.LevelDebug
	}

	targetLog := logFile
	if targetLog == "" {
		targetLog = cfg.LogFile
	}

	var logWriter io.Writer = os.Stderr
	if targetLog != "" {
		if err := os.MkdirAll(filepath.Dir(targetLog), 0o755); err == nil {
			f, err := os.OpenFile(targetLog, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
			if err == nil {
				defer f.Close()
				logWriter = io.MultiWriter(os.Stderr, f)
			}
		}
	}
	logger := slog.New(slog.NewTextHandler(logWriter, &slog.HandlerOptions{Level: logLevel}))

	transcriber, err := speech.Factory(cfg, logger)
	if err != nil {
		return err
	}
	if closer, ok := transcriber.(io.Closer); ok {
		defer closer.Close()
	}

	recDir := filepath.Join(os.TempDir(), "mavor-recordings")
	ov, err := overlay.NewDefault(cfg.TopMargin, logger)
	if err != nil {
		logger.Warn("overlay unavailable, falling back to noop", "err", err)
		ov = &overlay.Noop{}
	}

	recorder := audio.NewParecRecorder(recDir)
	recorder.SetLogger(logger)
	outDispatch := output.NewWayland()
	outDispatch.Logger = logger

	var ducker audio.Ducker = &audio.NoopDucker{}
	if cfg.DuckAudio {
		d := audio.NewCommandDucker(audio.BackendAuto, cfg.DuckVolume, cfg.DuckSink, cfg.DuckStreams)
		d.SetLogger(logger)
		ducker = d
	}

	d := daemon.New(daemon.Config{
		Socket:            cfg.Socket,
		Recorder:          recorder,
		Transcriber:       transcriber,
		Output:            outDispatch,
		Overlay:           ov,
		Ducker:            ducker,
		Logger:            logger,
		StreamingStrategy: cfg.StreamingStrategy,
		Mode:              cfg.Mode,
		History:           transcriptStore(logger),
		SilenceThreshold:  time.Duration(cfg.SilenceThresholdMS) * time.Millisecond,
		MinPhraseDuration: time.Duration(cfg.MinPhraseMS) * time.Millisecond,
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if starter, ok := transcriber.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			return fmt.Errorf("start transcriber engine: %w", err)
		}
	}

	modelPath := filepath.Join(cfg.ModelDir, "ggml-"+cfg.Model+".bin")
	logger.Info("daemon starting",
		"mode", cfg.Mode,
		"preset", cfg.Preset,
		"streaming_strategy", cfg.StreamingStrategy,
		"socket", cfg.Socket,
		"engine", cfg.Engine,
		"server_socket", cfg.ServerSocket,
		"model", cfg.Model,
		"model_path", modelPath,
		"gpu", cfg.GPU,
		"threads", cfg.Threads,
		"recording_dir", recDir,
		"top_margin", cfg.TopMargin,
		"duck_audio", cfg.DuckAudio,
		"duck_volume", cfg.DuckVolume,
		"duck_sink", cfg.DuckSink,
		"duck_streams", cfg.DuckStreams,
		"silence_threshold_ms", cfg.SilenceThresholdMS,
		"min_phrase_ms", cfg.MinPhraseMS,
		"pulse_source", os.Getenv("PULSE_SOURCE"),
		"wayland_display", os.Getenv("WAYLAND_DISPLAY"),
		"xdg_runtime_dir", os.Getenv("XDG_RUNTIME_DIR"),
		"log_file", logFile,
	)
	return d.Run(ctx)
}

func runToggle() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	resp, err := ipc.Send(cfg.Socket, ipc.Request{Action: "toggle"}, 2*time.Second)
	if err != nil {
		return fmt.Errorf("toggle: %w (is the daemon running?)", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Println(resp.State)
	return nil
}

func runStart() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	resp, err := ipc.Send(cfg.Socket, ipc.Request{Action: "start"}, 2*time.Second)
	if err != nil {
		return fmt.Errorf("start: %w (is the daemon running?)", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Println(resp.State)
	return nil
}

func runStop() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	resp, err := ipc.Send(cfg.Socket, ipc.Request{Action: "stop"}, 2*time.Second)
	if err != nil {
		return fmt.Errorf("stop: %w (is the daemon running?)", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Println(resp.State)
	return nil
}

func runStatus() error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	resp, err := ipc.Send(cfg.Socket, ipc.Request{Action: "status"}, 2*time.Second)
	if err != nil {
		return fmt.Errorf("status: %w (is the daemon running?)", err)
	}
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	fmt.Println(resp.State)
	return nil
}

// globSorted returns matches for pattern in a stable order, or nil. Glob's own
// error case is a malformed pattern, which is a programming mistake rather
// than a runtime condition, so it is discarded here.
func globSorted(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	sort.Strings(matches)
	return matches
}
