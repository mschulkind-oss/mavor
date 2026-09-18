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

func TestPasteEmitPrimaryWins(t *testing.T) {
	// Simulates Kitty terminal where Shift+Insert pastes from PRIMARY selection.
	// Primary exits first; clipboard must be killed and old selections restored.
	p := NewPaste(nil)
	l := newMockLauncher()
	p.Launcher = l

	// Existing selections
	l.runOutputs["wl-paste -n"] = []byte("existing clipboard content")
	l.runOutputs["wl-paste -p"] = []byte("existing primary content")

	// Trigger primary completion in background
	go func() {
		time.Sleep(10 * time.Millisecond)
		l.primCmd.waitCh <- nil
	}()

	err := p.Emit(context.Background(), "hello mavor")
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// Sibling (clipboard) must be killed
	if !l.clipCmd.isKilled() {
		t.Error("clipCmd was not killed when primary won")
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

func TestPasteEmitClipboardWins(t *testing.T) {
	// Simulates standard GUI app where Shift+Insert reads CLIPBOARD.
	// Clipboard exits first; primary must be killed and old selections restored.
	p := NewPaste(nil)
	l := newMockLauncher()
	p.Launcher = l

	l.runOutputs["wl-paste -n"] = []byte("old clip")
	l.runOutputs["wl-paste -p"] = []byte("old prim")

	go func() {
		time.Sleep(10 * time.Millisecond)
		l.clipCmd.waitCh <- nil
	}()

	err := p.Emit(context.Background(), "hello gui")
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	// Sibling (primary) must be killed
	if !l.primCmd.isKilled() {
		t.Error("primCmd was not killed when clipboard won")
	}
}

func TestPasteEmitTimeoutTerminatesBoth(t *testing.T) {
	p := NewPaste(nil)
	p.Timeout = 20 * time.Millisecond
	l := newMockLauncher()
	p.Launcher = l

	err := p.Emit(context.Background(), "hello timeout")
	if err != nil {
		t.Fatalf("Emit failed: %v", err)
	}

	if !l.clipCmd.isKilled() {
		t.Error("clipCmd was not killed on timeout")
	}
	if !l.primCmd.isKilled() {
		t.Error("primCmd was not killed on timeout")
	}
}

func TestPasteEmitNoRestore(t *testing.T) {
	p := NewPaste(nil)
	p.RestoreSelection = false
	l := newMockLauncher()
	p.Launcher = l

	go func() {
		time.Sleep(10 * time.Millisecond)
		l.clipCmd.waitCh <- nil
	}()

	if err := p.Emit(context.Background(), "hello no restore"); err != nil {
		t.Fatal(err)
	}

	for _, r := range l.runs {
		if r[0] == "wl-paste" {
			t.Errorf("wl-paste was called when RestoreSelection=false: %v", r)
		}
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
