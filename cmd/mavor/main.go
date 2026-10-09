package main

import (
	"context"
	"encoding/json"
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
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/output"
	"github.com/mschulkind-oss/mavor/internal/overlay"
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "print the daemon state and the stacked dictations",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runStatus(jsonOut) },
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit the whole status response as JSON")
	return cmd
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	recDir := filepath.Join(os.TempDir(), "mavor-recordings")
	ov, err := overlay.NewDefault(cfg.Overlay.TopMargin, cfg.Overlay.PreviewWidth, logger)
	if err != nil {
		logger.Warn("overlay unavailable, falling back to noop", "err", err)
		ov = &overlay.Noop{}
	}

	recorder := audio.NewParecRecorder(recDir)
	recorder.SetLogger(logger)
	outDispatch, closeOutput, err := selectOutput(cfg.Output, logger)
	if err != nil {
		return err
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

	finalMode, _ := models.ParseFinalMode(cfg.Advanced.FinalMode)
	d := daemon.New(daemon.Config{
		FinalMode:         finalMode,
		Socket:            cfg.Paths.Socket,
		Recorder:          recorder,
		Initialize:        func(ctx context.Context) (daemon.Initialized, error) { return initializeModels(ctx, cfg, logger) },
		Output:            outDispatch,
		Overlay:           ov,
		Ducker:            ducker,
		Logger:            logger,
		PreviewEnabled:    cfg.Preview.Enabled,
		History:           transcriptStore(logger),
		BinaryPath:        upgradeWatch,
		SilenceFilter:     cfg.Advanced.SilenceFilter,
		SilenceThreshold:  time.Duration(cfg.Preview.PauseMS) * time.Millisecond,
		MinPhraseDuration: time.Duration(cfg.Preview.MinPhraseMS) * time.Millisecond,
	})

	logger.Info("daemon starting", "socket", cfg.Paths.Socket, "model", cfg.Model, "gpu", cfg.Advanced.GPU, "cpu_fallback", cfg.Advanced.CPUFallback, "final_mode", cfg.Advanced.FinalMode)
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

func runStatus(jsonOut bool) error {
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "status"}, 2*time.Second)
	if err != nil {
		return fmt.Errorf("status: %w (is the daemon running?)", err)
	}
	if jsonOut {
		if resp.Error != "" {
			return errors.New(resp.Error)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	return writeStatus(os.Stdout, os.Stderr, resp)
}

// stackRowsShown is how many rows the status stack prints before collapsing the
// rest to a count — the same three visible rows the overlay reserves.
const stackRowsShown = 3

// writeStatus prints the derived state on stdout with the live dictations
// stacked beneath it — the recording row first, then one row per stopped cycle
// awaiting output — and provenance diagnostics on stderr, so an idle state does
// not erase a backup warning.
func writeStatus(out, diagnostic io.Writer, resp ipc.Response) error {
	if resp.Error != "" {
		return errors.New(resp.Error)
	}
	if _, err := fmt.Fprintln(out, resp.State); err != nil {
		return err
	}
	for _, row := range stackRows(resp) {
		if _, err := fmt.Fprintln(out, "  -", row); err != nil {
			return err
		}
	}
	if resp.Source != "" {
		if _, err := fmt.Fprintln(diagnostic, "source:", resp.Source); err != nil {
			return err
		}
	}
	if resp.Warning != "" {
		_, err := fmt.Fprintln(diagnostic, "warning:", resp.Warning)
		return err
	}
	return nil
}

// stackRows lists the live dictations in spoken order: the recording first,
// then one row per stopped cycle awaiting output. Beyond the visible rows the
// rest collapse to a count rather than growing without bound.
func stackRows(resp ipc.Response) []string {
	rows := make([]string, 0, resp.Transcribing+1)
	if resp.Recording {
		rows = append(rows, "recording")
	}
	for i := 0; i < resp.Transcribing; i++ {
		rows = append(rows, "transcribing")
	}
	if len(rows) > stackRowsShown {
		hidden := len(rows) - stackRowsShown
		rows = rows[:stackRowsShown]
		rows = append(rows, fmt.Sprintf("... and %d more", hidden))
	}
	return rows
}

// globSorted returns matches for pattern in a stable order, or nil. Glob's own
// error case is a malformed pattern, which is a programming mistake rather
// than a runtime condition, so it is discarded here.
func globSorted(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	sort.Strings(matches)
	return matches
}

// selectOutput keeps clipboard selection ahead of any keyboard constructor.
func selectOutput(cfg config.Output, logger *slog.Logger) (output.Dispatcher, func() error, error) {
	if err := cfg.ValidateDriver(); err != nil {
		return nil, nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Driver == "clipboard" {
		logger.Info("output: copy-only; paste manually", "clipboard_backend", cfg.ClipboardBackend)
		if cfg.ClipboardBackend == "x11" {
			c := output.NewX11Clipboard()
			return c, c.Close, nil
		}
		return output.NewClipboard(), nil, nil
	}
	if cfg.Driver == "paste" || cfg.Driver == "" {
		logger.Info("output: using paste dispatcher", "chord", cfg.PasteChord, "restore_selection", cfg.RestoreSelection)
		p := output.NewPaste(logger)
		p.Chord, p.CopyCommand = cfg.PasteChord, cfg.CopyCommand
		p.RestoreSelection, p.Clipboard = cfg.RestoreSelection, cfg.Clipboard
		return p, nil, nil
	}
	if native, err := output.NewNative(logger); err == nil {
		native.Clipboard = cfg.Clipboard
		return native, native.Close, nil
	} else {
		logger.Warn("output: falling back to wtype", "err", err)
	}
	w := output.NewWayland()
	w.Logger, w.Clipboard, w.TypingDelayMS = logger, cfg.Clipboard, cfg.TypingDelayMS
	return w, nil, nil
}
