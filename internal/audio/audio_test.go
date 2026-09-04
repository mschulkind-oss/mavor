package audio

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestMockRecorderRoundTrip(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "fixture.wav")
	if err := writeFakeWAV(wav); err != nil {
		t.Fatal(err)
	}
	m := &MockRecorder{FixturePath: wav}
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got, err := m.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got != wav {
		t.Fatalf("Stop returned %q, want %q", got, wav)
	}
}

func TestMockRecorderDoubleStartFails(t *testing.T) {
	m := &MockRecorder{}
	_ = m.Start(t.Context())
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("second Start should fail")
	}
}

func TestMockRecorderStopWithoutStartFails(t *testing.T) {
	m := &MockRecorder{}
	if _, err := m.Stop(); err == nil {
		t.Fatal("Stop without Start should fail")
	}
}

func TestParecRecorderWritesWAV(t *testing.T) {
	dir := t.TempDir()
	r := NewParecRecorder(dir)
	// Fake parec: write a fixed payload, ignore SIGINT, sleep until killed by
	// shell exit. The trap is what makes the test deterministic — without it
	// SIGINT can race past the printf and leave an empty file.
	r.SetCommand(func(out string) *exec.Cmd {
		// Mimic real parec: write a header immediately, then loop until
		// SIGINT, which we trap to exit cleanly with the WAV intact.
		return exec.Command("sh", "-c",
			`trap 'exit 0' INT; printf "RIFF....WAVEfmt fakebody" > "$1"; while :; do sleep 1; done`,
			"sh", out)
	})

	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Give the fake a moment to run printf before we send SIGINT (which the
	// trap will absorb, but Stop still sends it).
	time.Sleep(50 * time.Millisecond)

	wav, err := r.Stop()
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if filepath.Dir(wav) != dir {
		t.Fatalf("WAV %q not in dir %q", wav, dir)
	}
	body, err := readFile(wav)
	if err != nil || len(body) == 0 {
		t.Fatalf("WAV at %q is empty: err=%v", wav, err)
	}
}

func TestParecRecorderDoubleStartFails(t *testing.T) {
	r := NewParecRecorder(t.TempDir())
	r.SetCommand(func(out string) *exec.Cmd {
		return exec.Command("sh", "-c", `printf x > "$1"; sleep 60`, "sh", out)
	})
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _, _ = r.Stop() }()
	if err := r.Start(t.Context()); err == nil {
		t.Fatal("second Start should fail")
	}
}

func TestParecRecorderStopWithoutStartFails(t *testing.T) {
	r := NewParecRecorder(t.TempDir())
	if _, err := r.Stop(); err == nil {
		t.Fatal("Stop without Start should fail")
	}
}

func TestParecRecorderEmptyWAVIsAnError(t *testing.T) {
	r := NewParecRecorder(t.TempDir())
	// A command that exits immediately without writing anything.
	r.SetCommand(func(string) *exec.Cmd { return exec.Command("sh", "-c", "exit 0") })
	if err := r.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := r.Stop(); err == nil {
		t.Fatal("Stop on empty WAV should error")
	}
}

func TestMockRecorderLevel(t *testing.T) {
	m := &MockRecorder{}
	if lvl := m.Level(); lvl != 0.0 {
		t.Errorf("Level() before Start = %v, want 0.0", lvl)
	}
	m.SetLevel(0.75)
	if lvl := m.Level(); lvl != 0.0 {
		t.Errorf("Level() before Start with SetLevel = %v, want 0.0", lvl)
	}

	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if lvl := m.Level(); lvl != 0.75 {
		t.Errorf("Level() during capture = %v, want 0.75", lvl)
	}

	m.SetLevel(0.42)
	if lvl := m.Level(); lvl != 0.42 {
		t.Errorf("Level() during capture = %v, want 0.42", lvl)
	}

	if _, err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if lvl := m.Level(); lvl != 0.0 {
		t.Errorf("Level() after Stop = %v, want 0.0", lvl)
	}
}

func TestParecRecorderLevel(t *testing.T) {
	r := NewParecRecorder(t.TempDir())
	if lvl := r.Level(); lvl != 0.0 {
		t.Errorf("Level() before Start = %v, want 0.0", lvl)
	}
	r.SetLevel(0.5)
	if lvl := r.Level(); lvl != 0.0 {
		t.Errorf("Level() before Start with SetLevel = %v, want 0.0", lvl)
	}
}

func TestMockRecorderReadChunk(t *testing.T) {
	m := &MockRecorder{}

	// Not started
	chunk, err := m.ReadChunk()
	if err != nil || chunk != nil {
		t.Fatalf("ReadChunk before Start = (%v, %v), want (nil, nil)", chunk, err)
	}

	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Default chunk
	chunk, err = m.ReadChunk()
	if err != nil || len(chunk) == 0 {
		t.Fatalf("ReadChunk during active capture = (%v, %v)", chunk, err)
	}

	// Custom ChunkData
	m.SetChunkData([]byte{1, 2, 3, 4})
	chunk, err = m.ReadChunk()
	if err != nil || !reflect.DeepEqual(chunk, []byte{1, 2, 3, 4}) {
		t.Fatalf("ReadChunk with ChunkData = (%v, %v), want [1, 2, 3, 4]", chunk, err)
	}

	// Sequential Chunks
	m.SetChunks([]byte("chunk1"), []byte("chunk2"))
	c1, _ := m.ReadChunk()
	c2, _ := m.ReadChunk()
	c3, _ := m.ReadChunk()
	if string(c1) != "chunk1" || string(c2) != "chunk2" || c3 != nil {
		t.Fatalf("Sequential ReadChunk got (%q, %q, %v), want (chunk1, chunk2, nil)", string(c1), string(c2), c3)
	}

	if _, err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
}
