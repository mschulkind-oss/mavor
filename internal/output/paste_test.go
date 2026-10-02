package output

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChordToWtypeArgs(t *testing.T) {
	tests := []struct {
		chord   string
		want    []string
		wantErr bool
	}{
		{
			chord: "shift+insert",
			want:  []string{"-M", "shift", "-k", "Insert", "-m", "shift"},
		},
		{
			chord: "Shift+Insert",
			want:  []string{"-M", "shift", "-k", "Insert", "-m", "shift"},
		},
		{
			chord: "ctrl+shift+v",
			want:  []string{"-M", "ctrl", "-M", "shift", "-k", "v", "-m", "shift", "-m", "ctrl"},
		},
		{
			chord: "Ctrl+V",
			want:  []string{"-M", "ctrl", "-k", "v", "-m", "ctrl"},
		},
		{
			chord: "control+v",
			want:  []string{"-M", "ctrl", "-k", "v", "-m", "ctrl"},
		},
		{
			chord: "super+v",
			want:  []string{"-M", "logo", "-k", "v", "-m", "logo"},
		},
		{
			chord: "alt+v",
			want:  []string{"-M", "alt", "-k", "v", "-m", "alt"},
		},
		{
			chord: "insert",
			want:  []string{"-k", "Insert"},
		},
		{
			chord: "",
			want:  []string{"-M", "shift", "-k", "Insert", "-m", "shift"},
		},
		{
			chord:   "shift+",
			wantErr: true,
		},
		{
			chord:   "shift+ctrl",
			wantErr: true,
		},
		{
			chord:   "shift+insert+v",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.chord, func(t *testing.T) {
			got, err := ChordToWtypeArgs(tc.chord)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ChordToWtypeArgs(%q) err = %v, wantErr = %v", tc.chord, err, tc.wantErr)
			}
			if !tc.wantErr && !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ChordToWtypeArgs(%q) = %v, want %v", tc.chord, got, tc.want)
			}
		})
	}
}

type mockCmd struct {
	waitCh chan error
	killCh chan struct{}
	killed bool
	reaped bool
	mu     sync.Mutex
}

func newMockCmd() *mockCmd {
	return &mockCmd{
		waitCh: make(chan error, 1),
		killCh: make(chan struct{}, 1),
	}
}

func (m *mockCmd) Wait() error {
	var err error
	select {
	case err = <-m.waitCh:
	case <-m.killCh:
		err = errors.New("killed")
	}
	m.mu.Lock()
	m.reaped = true
	m.mu.Unlock()
	return err
}

func (m *mockCmd) Kill() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.killed {
		m.killed = true
		close(m.killCh)
	}
	return nil
}

func (m *mockCmd) isReaped() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reaped
}

func (m *mockCmd) isKilled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.killed
}

type mockLauncher struct {
	mu sync.Mutex

	// Responses for Run calls based on "name + first_arg"
	runOutputs map[string][]byte
	runErrors  map[string]error

	// Records of calls
	runs   [][]string
	starts [][]string

	// ManagedCmds returned for Start calls
	clipCmd *mockCmd
	primCmd *mockCmd
}

func newMockLauncher() *mockLauncher {
	return &mockLauncher{
		runOutputs: make(map[string][]byte),
		runErrors:  make(map[string]error),
		clipCmd:    newMockCmd(),
		primCmd:    newMockCmd(),
	}
}

func (m *mockLauncher) Run(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	call := append([]string{name}, args...)
	if stdin != nil {
		call = append(call, "(stdin:"+string(stdin)+")")
	}
	m.runs = append(m.runs, call)

	key := name
	if len(args) > 0 {
		key += " " + args[0]
	}
	return m.runOutputs[key], m.runErrors[key]
}

func (m *mockLauncher) Start(_ context.Context, stdin []byte, name string, args ...string) (ManagedCmd, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	call := append([]string{name}, args...)
	if stdin != nil {
		call = append(call, "(stdin:"+string(stdin)+")")
	}
	m.starts = append(m.starts, call)

	// Return clipCmd or primCmd based on whether --primary is in args
	for _, a := range args {
		if a == "--primary" {
			return m.primCmd, nil
		}
	}
	return m.clipCmd, nil
}

func TestPasteEmitEmptyText(t *testing.T) {
	p := NewPaste(nil)
	l := newMockLauncher()
	p.Launcher = l

	if err := p.Emit(context.Background(), "   \n\t  "); err != nil {
		t.Fatal(err)
	}
	if len(l.runs) > 0 || len(l.starts) > 0 {
		t.Errorf("empty text executed commands: runs=%v, starts=%v", l.runs, l.starts)
	}
}

func TestPasteEmitTimedLease(t *testing.T) {
	// Simulates the timed lease where both CLIPBOARD and PRIMARY selection
	// holders run in the foreground without --paste-once, allowing simultaneous
	// readers (e.g. cliphist and Kitty), terminating both after the lease duration.
	p := NewPaste(nil)
	p.LeaseDuration = 20 * time.Millisecond
	p.RestoreDelay = 1 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	// Existing selections
	l.runOutputs["wl-paste -n"] = []byte("existing clipboard content")
	l.runOutputs["wl-paste -p"] = []byte("existing primary content")

	start := time.Now()
	err := p.Emit(context.Background(), "hello timed lease")
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed < 20*time.Millisecond {
		t.Errorf("Emit returned in %v, expected at least lease duration of 20ms", elapsed)
	}

	// Verify that neither command used --paste-once
	if len(l.starts) != 2 {
		t.Fatalf("expected 2 started commands, got %d: %v", len(l.starts), l.starts)
	}
	for _, s := range l.starts {
		for _, arg := range s {
			if arg == "--paste-once" || arg == "-o" {
				t.Errorf("command %v used --paste-once; timed lease must not use paste-once", s)
			}
		}
	}

	// Both holders must be killed when the lease expires
	if !l.clipCmd.isKilled() {
		t.Error("clipCmd was not killed after lease expiration")
	}
	if !l.primCmd.isKilled() {
		t.Error("primCmd was not killed after lease expiration")
	}

	// Verify restore calls were made
	var restoredClip, restoredPrim bool
	for _, r := range l.runs {
		if len(r) >= 2 && r[0] == "wl-copy" && r[1] == "--primary" {
			restoredPrim = true
		} else if len(r) >= 1 && r[0] == "wl-copy" && (len(r) == 1 || r[1] != "--primary") {
			restoredClip = true
		}
	}
	if !restoredClip {
		t.Error("clipboard was not restored")
	}
	if !restoredPrim {
		t.Error("primary was not restored")
	}
}

func TestPasteEmitFallbackTimeout(t *testing.T) {
	p := NewPaste(nil)
	p.LeaseDuration = 0
	p.Timeout = 20 * time.Millisecond
	p.RestoreDelay = 1 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	err := p.Emit(context.Background(), "hello timeout fallback")
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	if !l.clipCmd.isKilled() {
		t.Error("clipCmd was not killed on fallback timeout")
	}
	if !l.primCmd.isKilled() {
		t.Error("primCmd was not killed on fallback timeout")
	}
}

func TestPasteEmitNoRestore(t *testing.T) {
	p := NewPaste(nil)
	p.RestoreSelection = false
	p.LeaseDuration = 10 * time.Millisecond
	p.RestoreDelay = 1 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	if err := p.Emit(context.Background(), "hello no restore"); err != nil {
		t.Fatal(err)
	}

	for _, r := range l.runs {
		if r[0] == "wl-paste" {
			t.Errorf("wl-paste was called when RestoreSelection=false: %v", r)
		}
	}
	if !l.clipCmd.isKilled() || !l.primCmd.isKilled() {
		t.Error("both commands must be killed even when RestoreSelection=false")
	}
}

func TestPasteEmitClipboardFlag(t *testing.T) {
	p := NewPaste(nil)
	p.Clipboard = true
	p.LeaseDuration = 10 * time.Millisecond
	p.RestoreDelay = 1 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	if err := p.Emit(context.Background(), "persist to clipboard"); err != nil {
		t.Fatal(err)
	}

	var copiedTranscript bool
	for _, r := range l.runs {
		if r[0] == "wl-copy" {
			for _, arg := range r {
				if arg == "(stdin:persist to clipboard)" {
					copiedTranscript = true
				}
			}
		}
	}
	if !copiedTranscript {
		t.Errorf("transcript was not persisted to clipboard with Clipboard=true; runs: %v", l.runs)
	}
}

func TestPasteEmitContextCancelled(t *testing.T) {
	p := NewPaste(nil)
	p.LeaseDuration = 500 * time.Millisecond
	p.RestoreDelay = 1 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := p.Emit(ctx, "cancelled context")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled error, got: %v", err)
	}

	if !l.clipCmd.isKilled() {
		t.Error("clipCmd was not killed on context cancellation")
	}
	if !l.primCmd.isKilled() {
		t.Error("primCmd was not killed on context cancellation")
	}
}

func TestPasteEmitCustomCopyCommand(t *testing.T) {
	p := NewPaste(nil)
	p.CopyCommand = []string{"xclip", "-selection", "clipboard"}
	l := newMockLauncher()
	p.Launcher = l

	if err := p.Emit(context.Background(), "custom copy text"); err != nil {
		t.Fatal(err)
	}

	if len(l.starts) > 0 {
		t.Errorf("supervisor started commands in custom mode: %v", l.starts)
	}

	var customRun, wtypeRun bool
	for _, r := range l.runs {
		if r[0] == "xclip" {
			customRun = true
		}
		if r[0] == "wtype" {
			wtypeRun = true
		}
	}
	if !customRun {
		t.Error("custom copy command was not executed")
	}
	if !wtypeRun {
		t.Error("wtype chord was not executed")
	}
}

func TestRealLauncherRunEcho(t *testing.T) {
	r := RealLauncher{}
	out, err := r.Run(context.Background(), nil, "sh", "-c", "echo 'hello launcher'")
	if err != nil {
		t.Fatalf("RealLauncher.Run failed: %v", err)
	}
	if string(out) != "hello launcher\n" {
		t.Errorf("got %q, want %q", string(out), "hello launcher\n")
	}
}

// deadlineLauncher uses real context deadlines, not simulated error responses.
type deadlineLauncher struct {
	*mockLauncher
	delays        [2]time.Duration
	restored      [2]bool
	exhaustFirst  bool
	restoreErrors [2]error
}

func (l *deadlineLauncher) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if name == "wl-copy" {
		i := 0
		if len(args) > 0 && args[0] == "--primary" {
			i = 1
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("cleanup missing deadline")
		}
		delay := l.delays[i]
		if i == 0 && l.exhaustFirst {
			deadline, _ := ctx.Deadline()
			delay = time.Until(deadline) - 150*time.Millisecond
		}
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if l.restoreErrors[i] != nil {
				return nil, l.restoreErrors[i]
			}
			l.restored[i] = true
		}
	}
	return l.mockLauncher.Run(ctx, stdin, name, args...)
}

func TestPasteRestoreIndependentBudgets(t *testing.T) {
	l := &deadlineLauncher{mockLauncher: newMockLauncher(), delays: [2]time.Duration{0, 250 * time.Millisecond}, exhaustFirst: true}
	l.runOutputs["wl-paste -n"] = []byte("prior clipboard")
	l.runOutputs["wl-paste -p"] = []byte("prior primary")
	p := NewPaste(nil)
	p.Launcher, p.LeaseDuration, p.RestoreDelay = l, time.Millisecond, time.Millisecond
	if err := p.Emit(context.Background(), "final text"); err != nil {
		t.Fatal(err)
	}
	if l.restored != [2]bool{true, true} {
		t.Fatalf("restored = %v; each restore fits its launch deadline, but shared budget starves PRIMARY", l.restored)
	}
}

func TestPasteRestoreSlowOrErrorStillRestoresPrimary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		delay time.Duration
		err   error
	}{
		{"deadline", ClipboardLaunchTimeout + time.Second, context.DeadlineExceeded},
		{"error", 0, errors.New("copy failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &deadlineLauncher{mockLauncher: newMockLauncher(), delays: [2]time.Duration{tc.delay, 0}}
			if tc.name == "error" {
				l.restoreErrors[0] = tc.err
			}
			l.runOutputs["wl-paste -n"] = []byte("prior clipboard")
			l.runOutputs["wl-paste -p"] = []byte("prior primary")
			var logs bytes.Buffer
			p := NewPaste(slog.New(slog.NewTextHandler(&logs, nil)))
			p.Launcher, p.LeaseDuration, p.RestoreDelay = l, time.Millisecond, time.Millisecond
			start := time.Now()
			err := p.Emit(context.Background(), "final text")
			if !errors.Is(err, tc.err) {
				t.Fatalf("Emit error = %v, want %v", err, tc.err)
			}
			if time.Since(start) > ClipboardLaunchTimeout+2*time.Second {
				t.Fatal("cleanup exceeded bounded launch budget plus scheduling allowance")
			}
			if l.restored != [2]bool{false, true} {
				t.Fatalf("restored = %v; failed CLIPBOARD must not prevent PRIMARY restoration", l.restored)
			}
			if !strings.Contains(logs.String(), "selection cleanup failed") || !strings.Contains(logs.String(), "selection=CLIPBOARD") {
				t.Fatalf("missing cleanup error: %s", &logs)
			}
			if !l.clipCmd.isKilled() || !l.primCmd.isKilled() {
				t.Fatal("holders not killed")
			}
		})
	}
}

func TestPasteRestoreEmptyAndFailedSnapshots(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(fmt.Sprint("failed=", failed), func(t *testing.T) {
			l := newMockLauncher()
			if failed {
				l.runErrors["wl-paste -n"] = errors.New("unreadable clipboard")
			}
			l.runOutputs["wl-paste -p"] = []byte(" primary with trailing whitespace \n")
			var logs bytes.Buffer
			p := NewPaste(slog.New(slog.NewTextHandler(&logs, nil)))
			p.Launcher, p.LeaseDuration, p.RestoreDelay = l, time.Millisecond, time.Millisecond
			if err := p.Emit(context.Background(), "final text"); err != nil {
				t.Fatal(err)
			}
			var writes [][]string
			for _, r := range l.runs {
				if r[0] == "wl-copy" {
					writes = append(writes, r)
				}
			}
			want := [][]string{{"wl-copy", "--clear"}, {"wl-copy", "--primary", "--type", "text/plain;charset=utf-8", "(stdin: primary with trailing whitespace \n)"}}
			if failed {
				want = want[1:]
			}
			if !reflect.DeepEqual(writes, want) {
				t.Fatalf("writes=%q want=%q", writes, want)
			}
			if failed && !strings.Contains(logs.String(), "selection restoration skipped") {
				t.Fatalf("missing snapshot failure: %s", &logs)
			}
		})
	}
}

func TestPasteCancellationRestoresWithIndependentContext(t *testing.T) {
	l := &deadlineLauncher{mockLauncher: newMockLauncher()}
	l.runOutputs["wl-paste -n"] = []byte("prior clipboard")
	l.runOutputs["wl-paste -p"] = []byte("prior primary")
	p := NewPaste(nil)
	p.Launcher, p.LeaseDuration, p.RestoreDelay = l, time.Second, time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel at chord injection, after snapshots and both holder launches.
	p.Launcher = &cancelChordLauncher{deadlineLauncher: l, cancel: cancel}
	if err := p.Emit(ctx, "final text"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Emit = %v", err)
	}
	if l.restored != [2]bool{true, true} {
		t.Fatalf("cancelled cleanup: %v", l.restored)
	}
	if !l.clipCmd.isKilled() || !l.primCmd.isKilled() || !l.clipCmd.isReaped() || !l.primCmd.isReaped() {
		t.Fatal("holders not killed and reaped")
	}
}

type cancelChordLauncher struct {
	*deadlineLauncher
	cancel context.CancelFunc
}

func (l *cancelChordLauncher) Run(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if name == "wtype" {
		l.cancel()
	}
	return l.deadlineLauncher.Run(ctx, stdin, name, args...)
}

func TestPasteCancelledCleanupErrorPreservesCancellation(t *testing.T) {
	failure := errors.New("primary restore failed")
	l := &deadlineLauncher{mockLauncher: newMockLauncher(), restoreErrors: [2]error{nil, failure}}
	l.runOutputs["wl-paste -n"] = []byte("prior clipboard")
	// Successful zero-byte PRIMARY snapshot must clear, not skip restoration.
	ctx, cancel := context.WithCancel(context.Background())
	p := NewPaste(nil)
	p.Launcher = &cancelChordLauncher{deadlineLauncher: l, cancel: cancel}
	p.LeaseDuration, p.RestoreDelay = time.Second, time.Millisecond
	err := p.Emit(ctx, "final text")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, failure) {
		t.Fatalf("Emit must preserve cancellation and cleanup failure: %v", err)
	}
	if l.restored != [2]bool{true, false} {
		t.Fatalf("restored = %v", l.restored)
	}
}

func TestPasteRestorePrimaryEmpty(t *testing.T) {
	l := newMockLauncher()
	l.runErrors["wl-paste -n"] = errors.New("unreadable clipboard")
	p := NewPaste(nil)
	p.Launcher, p.LeaseDuration, p.RestoreDelay = l, time.Millisecond, time.Millisecond
	if err := p.Emit(context.Background(), "final text"); err != nil {
		t.Fatal(err)
	}
	var writes [][]string
	for _, r := range l.runs {
		if r[0] == "wl-copy" {
			writes = append(writes, r)
		}
	}
	if !reflect.DeepEqual(writes, [][]string{{"wl-copy", "--primary", "--clear"}}) {
		t.Fatalf("only known-empty PRIMARY should be cleared: %v", writes)
	}
}
