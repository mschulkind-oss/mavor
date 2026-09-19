package output

import (
	"context"
	"errors"
	"reflect"
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
	mu     sync.Mutex
}

func newMockCmd() *mockCmd {
	return &mockCmd{
		waitCh: make(chan error, 1),
		killCh: make(chan struct{}, 1),
	}
}

func (m *mockCmd) Wait() error {
	select {
	case err := <-m.waitCh:
		return err
	case <-m.killCh:
		return errors.New("killed")
	}
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
