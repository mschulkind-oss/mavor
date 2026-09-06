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

	// Config is read once, here, and never reloaded: a change needs `mavor
	// stop` and a restart.
	cfgFile, err := config.LoadFile("")
	if err != nil {
		return err
	}
	cfg := cfgFile.Config
	// The flag wins over the config key: -v is what you reach for to debug
	// one run, and it would be surprising if a config file could refuse it.
	logLevel := slog.LevelInfo
	if verbose || cfg.Logging.Verbose {
		logLevel = slog.LevelDebug
	}

	targetLog := logFile
	if targetLog == "" {
		targetLog = cfg.Paths.Log
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

	// Anything in the file the schema does not have is reported here, once,
	// now that there is a logger to report it to. It is not fatal — but a
	// setting that is silently ignored is worse than one that is refused.
	cfgFile.LogWarnings(logger)

	// The model decides the runtime, the runtime and [advanced] decide the
	// placement, and both are resolved once here — config is read at daemon
	// start and never reloaded.
	resolved, err := speech.Resolve(cfg)
	if err != nil {
		return err
	}
	transcriber, err := speech.FactoryFor(cfg, resolved, logger)
	if err != nil {
		return err
	}
	if closer, ok := transcriber.(io.Closer); ok {
		defer closer.Close()
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Where the overlay's text comes from, decided here and once (§6.2). A
	// companion model is loaded now rather than at the first recording, so
	// the first dictation is not the slow one; a model NAMED in the config
	// and missing is the one preview failure that stops the daemon.
	preview, err := speech.LoadPreview(ctx, cfg, logger)
	if err != nil {
		return err
	}
	defer preview.Close()

	recDir := filepath.Join(os.TempDir(), "mavor-recordings")
	ov, err := overlay.NewDefault(cfg.Overlay.TopMargin, logger)
	if err != nil {
		logger.Warn("overlay unavailable, falling back to noop", "err", err)
		ov = &overlay.Noop{}
	}

	recorder := audio.NewParecRecorder(recDir)
	recorder.SetLogger(logger)
	outDispatch := output.NewWayland()
	outDispatch.Logger = logger
	outDispatch.Clipboard = cfg.Output.Clipboard

	var ducker audio.Ducker = &audio.NoopDucker{}
	if cfg.Ducking.Enabled {
		d := audio.NewCommandDucker(audio.BackendAuto, cfg.Ducking.Volume, cfg.Ducking.Sink, cfg.Ducking.Apps)
		d.SetLogger(logger)
		ducker = d
	}

	d := daemon.New(daemon.Config{
		Socket:            cfg.Paths.Socket,
		Recorder:          recorder,
		Transcriber:       transcriber,
		Output:            outDispatch,
		Overlay:           ov,
		Ducker:            ducker,
		Logger:            logger,
		PreviewEnabled:    cfg.Preview.Enabled,
		PreviewMode:       preview.Mode,
		PreviewCompanion:  preview.Companion,
		History:           transcriptStore(logger),
		SilenceThreshold:  time.Duration(cfg.Preview.PauseMS) * time.Millisecond,
		MinPhraseDuration: time.Duration(cfg.Preview.MinPhraseMS) * time.Millisecond,
	})

	if starter, ok := transcriber.(interface{ Start(context.Context) error }); ok {
		if err := starter.Start(ctx); err != nil {
			return fmt.Errorf("start transcriber engine: %w", err)
		}
	}

	logger.Info("daemon starting",
		"socket", cfg.Paths.Socket,
		"model", cfg.Model,
		"runtime", string(resolved.Runtime),
		"placement", string(resolved.Placement),
		"placement_reason", resolved.Reason,
		"model_path", resolved.ModelPath,
		"model_dir", resolved.ModelDir,
		"server", resolved.Server,
		"preview_enabled", cfg.Preview.Enabled,
		"preview_source", cfg.Preview.Source,
		"preview_mode", string(preview.Mode),
		"preview_companion", preview.Model,
		"preview_reason", preview.Reason,
		"gpu", cfg.Advanced.GPU,
		"threads", cfg.Advanced.Threads,
		"recording_dir", recDir,
		"top_margin", cfg.Overlay.TopMargin,
		"duck_enabled", cfg.Ducking.Enabled,
		"duck_volume", cfg.Ducking.Volume,
		"duck_sink", cfg.Ducking.Sink,
		"duck_apps", cfg.Ducking.Apps,
		"pause_ms", cfg.Preview.PauseMS,
		"min_phrase_ms", cfg.Preview.MinPhraseMS,
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
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "toggle"}, 2*time.Second)
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
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "start"}, 2*time.Second)
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
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "stop"}, 2*time.Second)
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
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "status"}, 2*time.Second)
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
