package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Architecture and invariants: docs/reference/paste-based-output-dispatch.md

// SelectionRegistrationTimeout bounds how long a dispatch waits for the two
// selection holders to actually own CLIPBOARD and PRIMARY before it injects the
// paste chord.
//
// The holders are separate processes. wl-copy has to be exec'd, link, connect to
// the compositor and call set_selection before a reader asking for the selection
// is served by it, and how long that takes is not a property of this program: a
// loaded machine can take longer than any constant. Injecting the chord before
// that happens hands the reader whoever owned the selection a moment earlier —
// typically the text the user copied before dictating, which is what "the wrong
// thing got pasted" looks like from the outside.
const SelectionRegistrationTimeout = 500 * time.Millisecond

// registrationPollInterval is the pause between read-backs while waiting for a
// holder to register. It grows up to registrationPollMax so a slow start costs a
// handful of reads rather than dozens.
const (
	registrationPollInterval = 2 * time.Millisecond
	registrationPollMax      = 16 * time.Millisecond
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
	cmd    *exec.Cmd
	stderr *bytes.Buffer
}

func (r *realCmd) Wait() error {
	err := r.cmd.Wait()
	if r.stderr != nil && r.stderr.Len() > 0 {
		return fmt.Errorf("%v (stderr: %s)", err, r.stderr.String())
	}
	return err
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
	if name == "wl-copy" {
		// wl-copy daemonizes itself to hold clipboard selection and leaves stderr open in the child.
		// Running without capturing child stderr prevents blocking indefinitely on pipe EOF.
		return nil, cmd.Run()
	}
	return cmd.CombinedOutput()
}

func (RealLauncher) Start(ctx context.Context, stdin []byte, name string, args ...string) (ManagedCmd, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &realCmd{cmd: cmd, stderr: &stderr}, nil
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

	// Timeout is the maximum time to wait for paste dispatch.
	Timeout time.Duration

	// LeaseDuration is the window during which CLIPBOARD and PRIMARY selection
	// holders remain active in the foreground to serve simultaneous consumers (e.g. cliphist
	// watching clipboard plus Kitty pasting primary). Defaults to 350ms.
	LeaseDuration time.Duration

	// RegistrationTimeout bounds the wait, after the holders start and before
	// the chord is injected, for both selections to actually serve the
	// transcript. Defaults to SelectionRegistrationTimeout.
	RegistrationTimeout time.Duration

	// RestoreDelay allows the target application to finish consuming in-flight
	// selection pipes before restoring previous selections.
	RestoreDelay time.Duration

	// Launcher executes commands. Defaults to RealLauncher{}.
	Launcher CommandLauncher
}

// NewPaste constructs a Paste dispatcher with default settings.
func NewPaste(logger *slog.Logger) *Paste {
	if logger == nil {
		logger = slog.Default()
	}
	return &Paste{
		Logger:              logger,
		Chord:               "shift+insert",
		RestoreSelection:    true,
		Clipboard:           false,
		LeaseDuration:       350 * time.Millisecond,
		RestoreDelay:        50 * time.Millisecond,
		RegistrationTimeout: SelectionRegistrationTimeout,
		Launcher:            RealLauncher{},
	}
}

// Emit sends text to the focused window using paste dispatch.
func (p *Paste) Emit(ctx context.Context, text string) (emitErr error) {
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

	restoreDelay := p.RestoreDelay
	if restoreDelay <= 0 {
		restoreDelay = 50 * time.Millisecond
	}

	defer func() {
		if p.RestoreSelection || p.Clipboard {
			time.Sleep(restoreDelay)
		}

		// Each selection gets a complete launch budget even if the other fails.
		// This is startup only: a successfully forked owner survives the deadline.
		write := func(selection string, data []byte, args ...string) {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), ClipboardLaunchTimeout)
			defer cancel()
			start := time.Now()
			_, err := p.Launcher.Run(cleanupCtx, data, "wl-copy", args...)
			if err = errors.Join(err, cleanupCtx.Err()); err != nil {
				log.Error("output: selection cleanup failed", "selection", selection,
					"elapsed_ms", time.Since(start).Milliseconds(), "err", err)
				emitErr = errors.Join(emitErr, fmt.Errorf("output: restore %s: %w", selection, err))
			}
		}
		restore := func(selection string, data []byte, snapshotErr error, args ...string) {
			if snapshotErr != nil {
				// An unreadable snapshot is not an intentionally empty selection.
				log.Warn("output: selection restoration skipped", "selection", selection, "snapshot_err", snapshotErr)
				return
			}
			if len(data) == 0 {
				args = append(args, "--clear")
			} else {
				args = append(args, "--type", "text/plain;charset=utf-8")
			}
			write(selection, data, args...)
		}
		if p.Clipboard {
			write("CLIPBOARD", []byte(text), "--type", "text/plain;charset=utf-8")
		} else if p.RestoreSelection {
			restore("CLIPBOARD", oldClip, clipErr)
		}
		if p.RestoreSelection {
			restore("PRIMARY", oldPrim, primErr, "--primary")
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

	// Dual-buffer Timed Lease:
	// Spawn both selection holders in the foreground (WITHOUT --paste-once).
	// Pass text as an argument to avoid pipe/stdin buffering and EOF timing issues.
	cmdClip, err := p.Launcher.Start(ctx, nil, "wl-copy", "--foreground", "--", text)
	if err != nil {
		return fmt.Errorf("output: failed to start wl-copy: %w", err)
	}
	cmdPrim, err := p.Launcher.Start(ctx, nil, "wl-copy", "--primary", "--foreground", "--", text)
	if err != nil {
		_ = cmdClip.Kill()
		_ = cmdClip.Wait()
		return fmt.Errorf("output: failed to start wl-copy --primary: %w", err)
	}

	// Wait until a reader asking for either selection would actually receive the
	// transcript, rather than sleeping for a guessed interval and hoping the
	// holders got there first. See SelectionRegistrationTimeout.
	registrationTimeout := p.RegistrationTimeout
	if registrationTimeout <= 0 {
		registrationTimeout = SelectionRegistrationTimeout
	}
	registrationStart := time.Now()
	registrationCtx, cancelRegistration := context.WithTimeout(ctx, registrationTimeout)
	clipReady, primReady := p.waitForSelections(registrationCtx, text)
	cancelRegistration()
	if !clipReady || !primReady {
		log.Warn("output: selection holders did not serve the transcript before the chord",
			"clipboard_ready", clipReady,
			"primary_ready", primReady,
			"waited_ms", time.Since(registrationStart).Milliseconds())
	}

	// Synthesize the paste chord into the focused window
	if _, err := p.Launcher.Run(ctx, nil, "wtype", wtypeArgs...); err != nil {
		log.Warn("output: wtype chord failed", "err", err)
	}

	leaseDuration := p.LeaseDuration
	if leaseDuration <= 0 {
		leaseDuration = p.Timeout
	}
	if leaseDuration <= 0 {
		leaseDuration = 350 * time.Millisecond
	}

	select {
	case <-time.After(leaseDuration):
		log.Info("output: paste lease completed", "duration_ms", leaseDuration.Milliseconds(), "elapsed_ms", time.Since(start).Milliseconds())
	case <-ctx.Done():
		log.Warn("output: context cancelled during paste lease")
	}

	_ = cmdClip.Kill()
	_ = cmdPrim.Kill()
	errClip := cmdClip.Wait()
	errPrim := cmdPrim.Wait()
	log.Info("output: holders reaped", "errClip", errClip, "errPrim", errPrim)

	return ctx.Err()
}

// waitForSelections blocks until CLIPBOARD and PRIMARY both serve text, or the
// context is done, and reports which of the two got there.
//
// The selections are polled independently and one that has already answered
// correctly is not read again, so a dispatch that is not racing anything pays
// one read per selection. Reading a selection does not take it away from its
// holder, so the read-back observes what a paste would receive without
// consuming it; a selection that serves text because some unrelated owner
// happens to hold the same bytes is also fine, since a reader would get the
// transcript either way.
func (p *Paste) waitForSelections(ctx context.Context, text string) (clipReady, primReady bool) {
	interval := registrationPollInterval
	for {
		if !clipReady {
			clipReady = p.selectionServes(ctx, text, "wl-paste", "-n")
		}
		if !primReady {
			primReady = p.selectionServes(ctx, text, "wl-paste", "-p", "-n")
		}
		if (clipReady && primReady) || ctx.Err() != nil {
			return clipReady, primReady
		}
		select {
		case <-ctx.Done():
			return clipReady, primReady
		case <-time.After(interval):
		}
		if interval < registrationPollMax {
			interval *= 2
		}
	}
}

// selectionServes reports whether the named selection currently hands a reader
// text. wl-paste -n strips the holder's trailing newline, so text that differs
// only by one still counts as served.
func (p *Paste) selectionServes(ctx context.Context, text, name string, args ...string) bool {
	got, err := p.Launcher.Run(ctx, nil, name, args...)
	if err != nil {
		return false
	}
	return strings.TrimRight(string(got), "\n") == strings.TrimRight(text, "\n")
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
