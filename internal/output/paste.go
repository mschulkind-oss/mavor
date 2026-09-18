package output

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// CommandLauncher abstracts process execution for the paste supervisor and tests.
type CommandLauncher interface {
	Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)
	Start(ctx context.Context, stdin []byte, name string, args ...string) (ManagedCmd, error)
}

// ManagedCmd represents a long-running child process (such as wl-copy -f -o).
type ManagedCmd interface {
	Wait() error
	Kill() error
}

type realCmd struct {
	cmd *exec.Cmd
}

func (r *realCmd) Wait() error {
	return r.cmd.Wait()
}

func (r *realCmd) Kill() error {
	if r.cmd.Process != nil {
		return r.cmd.Process.Kill()
	}
	return nil
}

// RealLauncher shells out via os/exec.
type RealLauncher struct{}

func (RealLauncher) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	return cmd.CombinedOutput()
}

func (RealLauncher) Start(ctx context.Context, stdin []byte, name string, args ...string) (ManagedCmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &realCmd{cmd: cmd}, nil
}

// ChordToWtypeArgs converts a human-readable chord (e.g. "shift+insert", "ctrl+shift+v")
// into wtype command line arguments that depress modifiers, press and release the target key,
// and release modifiers in reverse order.
func ChordToWtypeArgs(chord string) ([]string, error) {
	chord = strings.TrimSpace(chord)
	if chord == "" {
		chord = "shift+insert"
	}
	parts := strings.Split(chord, "+")
	var mods []string
	var key string

	for _, part := range parts {
		p := strings.ToLower(strings.TrimSpace(part))
		if p == "" {
			continue
		}
		switch p {
		case "shift":
			mods = append(mods, "shift")
		case "ctrl", "control":
			mods = append(mods, "ctrl")
		case "alt", "meta":
			mods = append(mods, "alt")
		case "altgr":
			mods = append(mods, "altgr")
		case "super", "logo", "win":
			mods = append(mods, "logo")
		case "capslock":
			mods = append(mods, "capslock")
		default:
			if key != "" {
				return nil, fmt.Errorf("chord %q contains multiple keys (%q and %q)", chord, key, p)
			}
			key = normalizeKeyName(p)
		}
	}

	if key == "" {
		return nil, fmt.Errorf("chord %q contains no key", chord)
	}

	var args []string
	for _, m := range mods {
		args = append(args, "-M", m)
	}
	args = append(args, "-k", key)
	for i := len(mods) - 1; i >= 0; i-- {
		args = append(args, "-m", mods[i])
	}
	return args, nil
}

func normalizeKeyName(key string) string {
	switch strings.ToLower(key) {
	case "insert", "ins":
		return "Insert"
	case "return", "enter":
		return "Return"
	case "escape", "esc":
		return "Escape"
	case "tab":
		return "Tab"
	case "space":
		return "space"
	case "backspace":
		return "BackSpace"
	case "delete", "del":
		return "Delete"
	default:
		if strings.HasPrefix(strings.ToLower(key), "f") && len(key) > 1 {
			return "F" + key[1:]
		}
		return key
	}
}

// Paste dispatches transcripts by placing text into Wayland selection buffers
// and synthesizing a paste chord into the focused window.
//
// In contrast to typing thousands of individual keystrokes via virtual keyboard,
// paste dispatch is ingested by terminal emulators and CLIs as a single Bracketed
// Paste chunk (\e[200~...\e[201~), rendering 1,000+ characters in under 15ms.
type Paste struct {
	Logger *slog.Logger

	// Chord is the paste chord to synthesize (e.g. "shift+insert", "ctrl+shift+v").
	Chord string

	// CopyCommand overrides the copy utility. If empty, defaults to dual-buffer wl-copy.
	CopyCommand []string

	// RestoreSelection restores previous clipboard and primary selections after paste.
	RestoreSelection bool

	// Clipboard ensures the transcript remains on the clipboard after dispatch.
	Clipboard bool

	// Timeout is the maximum time to wait for paste-once consumption.
	Timeout time.Duration

	// Launcher executes commands. Defaults to RealLauncher{}.
	Launcher CommandLauncher
}

// NewPaste constructs a Paste dispatcher with default settings.
func NewPaste(logger *slog.Logger) *Paste {
	if logger == nil {
		logger = slog.Default()
	}
	return &Paste{
		Logger:           logger,
		Chord:            "shift+insert",
		RestoreSelection: true,
		Clipboard:        false,
		Timeout:          1 * time.Second,
		Launcher:         RealLauncher{},
	}
}

// Emit sends text to the focused window using paste dispatch.
func (p *Paste) Emit(ctx context.Context, text string) error {
	log := p.Logger
	if log == nil {
		log = slog.Default()
	}
	text = CleanText(text)
	if text == "" {
		return nil
	}

	start := time.Now()
	log.Info("output: dispatching via paste",
		"text_len", len(text),
		"text_preview", truncate(text, 200),
		"chord", p.Chord,
		"restore", p.RestoreSelection)

	var oldClip, oldPrim []byte
	var clipErr, primErr error

	if p.RestoreSelection {
		// Read pre-existing selections to preserve user state
		oldClip, clipErr = p.Launcher.Run(ctx, nil, "wl-paste", "-n")
		oldPrim, primErr = p.Launcher.Run(ctx, nil, "wl-paste", "-p", "-n")
	}

	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		if p.Clipboard {
			// User requested transcript stay on clipboard
			_, _ = p.Launcher.Run(cleanupCtx, []byte(text), "wl-copy")
		} else if p.RestoreSelection && clipErr == nil {
			if len(oldClip) > 0 {
				_, _ = p.Launcher.Run(cleanupCtx, oldClip, "wl-copy")
			} else {
				_, _ = p.Launcher.Run(cleanupCtx, nil, "wl-copy", "--clear")
			}
		}

		if p.RestoreSelection && primErr == nil {
			if len(oldPrim) > 0 {
				_, _ = p.Launcher.Run(cleanupCtx, oldPrim, "wl-copy", "--primary")
			} else {
				_, _ = p.Launcher.Run(cleanupCtx, nil, "wl-copy", "--primary", "--clear")
			}
		}
	}()

	wtypeArgs, err := ChordToWtypeArgs(p.Chord)
	if err != nil {
		return fmt.Errorf("output: invalid paste chord %q: %w", p.Chord, err)
	}

	if len(p.CopyCommand) > 0 {
		cmdName := p.CopyCommand[0]
		cmdArgs := p.CopyCommand[1:]
		if _, err := p.Launcher.Run(ctx, []byte(text), cmdName, cmdArgs...); err != nil {
			return fmt.Errorf("output: custom copy command failed: %w", err)
		}
		if _, err := p.Launcher.Run(ctx, nil, "wtype", wtypeArgs...); err != nil {
			return fmt.Errorf("output: wtype chord failed: %w", err)
		}
		return nil
	}

	// Dual-buffer First-Exit Wins Supervisor:
	// Spawn both selection holders in the foreground with --paste-once.
	cmdClip, err := p.Launcher.Start(ctx, []byte(text), "wl-copy", "--foreground", "--paste-once")
	if err != nil {
		return fmt.Errorf("output: failed to start wl-copy: %w", err)
	}
	cmdPrim, err := p.Launcher.Start(ctx, []byte(text), "wl-copy", "--primary", "--foreground", "--paste-once")
	if err != nil {
		_ = cmdClip.Kill()
		_ = cmdClip.Wait()
		return fmt.Errorf("output: failed to start wl-copy --primary: %w", err)
	}

	// Allow child processes a brief moment to register Wayland selections before chord injection
	time.Sleep(5 * time.Millisecond)

	// Synthesize the paste chord into the focused window
	if _, err := p.Launcher.Run(ctx, nil, "wtype", wtypeArgs...); err != nil {
		log.Warn("output: wtype chord failed", "err", err)
	}

	done := make(chan string, 2)
	go func() {
		_ = cmdClip.Wait()
		done <- "clipboard"
	}()
	go func() {
		_ = cmdPrim.Wait()
		done <- "primary"
	}()

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 1 * time.Second
	}

	select {
	case winner := <-done:
		log.Info("output: paste-once consumed", "winner", winner, "elapsed_ms", time.Since(start).Milliseconds())
		if winner == "clipboard" {
			_ = cmdPrim.Kill()
			_ = cmdPrim.Wait()
		} else {
			_ = cmdClip.Kill()
			_ = cmdClip.Wait()
		}
	case <-time.After(timeout):
		log.Warn("output: paste-once timeout expired without consumption; terminating holders", "timeout", timeout)
		_ = cmdClip.Kill()
		_ = cmdPrim.Kill()
		_ = cmdClip.Wait()
		_ = cmdPrim.Wait()
	case <-ctx.Done():
		log.Warn("output: context cancelled during paste dispatch")
		_ = cmdClip.Kill()
		_ = cmdPrim.Kill()
		_ = cmdClip.Wait()
		_ = cmdPrim.Wait()
		return ctx.Err()
	}

	return nil
}

// CopyOnly copies text directly to the clipboard without synthesizing a paste chord.
func (p *Paste) CopyOnly(ctx context.Context, text string) error {
	_, err := p.Launcher.Run(ctx, []byte(text), "wl-copy")
	return err
}

// Close implements cleanup if needed.
func (p *Paste) Close() error {
	return nil
}
