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

	"github.com/spf13/cobra"

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
	exit(newRootCmd().Execute())
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

func newDaemonCmd() *cobra.Command {
	var (
		verbose bool
		logFile string
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "run the long-lived dictation service",
		Long: "Run the long-lived dictation service.\n\n" +
			"Config is read once at start and never reloaded: a change needs a restart.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runDaemon(verbose, logFile)
		},
	}
	cmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "log at debug level")
	cmd.Flags().StringVar(&logFile, "log-file", "", "write the daemon log here instead of the configured path")
	return cmd
}

func newToggleCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "toggle",
		Short: "toggle recording on or off",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runToggle() },
	}
}

func newStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "begin recording (push-to-talk: bind to key press)",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runStart() },
	}
}

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "end recording and transcribe (push-to-talk: bind to key release)",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runStop() },
	}
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "print the daemon state (idle, recording, transcribing)",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runStatus() },
	}
}

func runDaemon(verbose bool, logFile string) error {
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
	// The main model's engine comes up concurrently with the companion's:
	// nothing about either load depends on the other, and doing them in
	// series doubled the wait before the daemon was usable.
	startedAt := time.Now()
	waitForEngine := beginStart(ctx, transcriber)

	preview, err := speech.LoadPreview(ctx, cfg, logger)
	if err != nil {
		waitOrLog(waitForEngine, logger.Warn)
		return err
	}
	defer preview.Close()

	recDir := filepath.Join(os.TempDir(), "mavor-recordings")
	ov, err := overlay.NewDefault(cfg.Overlay.TopMargin, cfg.Overlay.PreviewWidth, logger)
	if err != nil {
		logger.Warn("overlay unavailable, falling back to noop", "err", err)
		ov = &overlay.Noop{}
	}

	recorder := audio.NewParecRecorder(recDir)
	recorder.SetLogger(logger)
	// Type in-process where the compositor allows it, and fall back to wtype
	// where it does not. Both need the same protocol, so the fallback is for
	// a missing binding rather than a missing feature — but it keeps a
	// working path if the in-process one ever misbehaves.
	var outDispatch output.Dispatcher
	var closeOutput func() error
	if native, err := output.NewNative(logger); err == nil {
		native.Clipboard = cfg.Output.Clipboard
		outDispatch, closeOutput = native, native.Close
	} else {
		logger.Warn("output: falling back to wtype", "err", err)
		w := output.NewWayland()
		w.Logger = logger
		w.Clipboard = cfg.Output.Clipboard
		w.TypingDelayMS = cfg.Output.TypingDelayMS
		outDispatch = w
	}
	if closeOutput != nil {
		defer func() { _ = closeOutput() }()
	}

	// Two places audio comes from, ducked independently: the sound server for
	// anything playing on this machine, and an X Air mixer for a monitor mix
	// that never passes through it. A user can have either, both or neither.
	var duckers audio.Duckers
	if cfg.Ducking.Enabled {
		d := audio.NewCommandDucker(audio.BackendAuto, cfg.Ducking.Volume, cfg.Ducking.Sink, cfg.Ducking.Apps)
		d.SetLogger(logger)
		duckers = append(duckers, d)
	}
	if o := cfg.Ducking.OSC; o.Enabled {
		x, err := audio.NewOSCDucker(o.Address, o.Port, o.Paths, o.MutedValue,
			time.Duration(o.TimeoutMS)*time.Millisecond)
		if err != nil {
			// Misconfiguration, not a missing device: the address or the path
			// list is unusable, and no amount of retrying fixes it. Refusing
			// to start would take dictation down with it, so say so loudly
			// and carry on without it.
			logger.Error("osc: ducking disabled — the [ducking.osc] config is not usable", "err", err)
		} else {
			x.SetLogger(logger)
			defer func() { _ = x.Close() }()
			duckers = append(duckers, x)
			logger.Info("osc: device ducking enabled",
				"addr", x.Addr(), "paths", x.Paths(),
				"muted_value", x.MutedValue(), "timeout_ms", o.TimeoutMS)
		}
	}
	var ducker audio.Ducker = &audio.NoopDucker{}
	if len(duckers) > 0 {
		ducker = duckers
	}

	// Watched only under a supervisor that will start the replacement; see
	// restartOnUpgrade. getBinaryPath is the same path `mavor service
	// install` writes into ExecStart, which is the one that outlives the
	// upgrade.
	upgradeWatch := ""
	if restartOnUpgrade() {
		upgradeWatch = getBinaryPath()
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
		BinaryPath:        upgradeWatch,
		SilenceThreshold:  time.Duration(cfg.Preview.PauseMS) * time.Millisecond,
		MinPhraseDuration: time.Duration(cfg.Preview.MinPhraseMS) * time.Millisecond,
	})

	if err := waitForEngine(); err != nil {
		return err
	}
	logger.Info("models: ready", "took", time.Since(startedAt))

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
		"duck_osc_enabled", cfg.Ducking.OSC.Enabled,
		"duck_osc_address", cfg.Ducking.OSC.Address,
		"duck_osc_port", cfg.Ducking.OSC.Port,
		"duck_osc_paths", cfg.Ducking.OSC.Paths,
		"pause_ms", cfg.Preview.PauseMS,
		"min_phrase_ms", cfg.Preview.MinPhraseMS,
		"pulse_source", os.Getenv("PULSE_SOURCE"),
		"wayland_display", os.Getenv("WAYLAND_DISPLAY"),
		"xdg_runtime_dir", os.Getenv("XDG_RUNTIME_DIR"),
		"log_file", logFile,
		"upgrade_watch", upgradeWatch,
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
