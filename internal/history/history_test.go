package history

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAppendAndRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path}

	for _, txt := range []string{"first one", "second one", "third one"} {
		if err := s.Append(Entry{Text: txt, At: time.Now()}); err != nil {
			t.Fatalf("Append(%q): %v", txt, err)
		}
	}

	got, err := s.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Recent returned %d entries, want 3", len(got))
	}
	// Newest first — a recovery tool wants the thing that just went missing.
	if got[0].Text != "third one" || got[2].Text != "first one" {
		t.Errorf("Recent order = %q..%q, want newest first", got[0].Text, got[2].Text)
	}
}

func TestRecentLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path}
	for i := range 5 {
		if err := s.Append(Entry{Text: string(rune('a' + i))}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := s.Recent(2)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Recent(2) returned %d, want 2", len(got))
	}
	if got[0].Text != "e" {
		t.Errorf("Recent(2)[0] = %q, want the newest entry", got[0].Text)
	}
}

// Multi-line transcripts must survive the round trip intact — a newline in the
// text must not be mistaken for a record separator.
func TestMultilineTextRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path}
	want := "line one\nline two\twith a tab"
	if err := s.Append(Entry{Text: want}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 1 || got[0].Text != want {
		t.Fatalf("round trip = %q, want %q", got, want)
	}
}

// A missing file is the normal first-run state, not an error.
func TestRecentOnMissingFile(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "nope.jsonl")}
	got, err := s.Recent(0)
	if err != nil {
		t.Fatalf("Recent on missing file: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d entries, want 0", len(got))
	}
}

// A truncated or corrupt line must not cost the user the rest of their history.
func TestRecentSkipsCorruptLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path}
	if err := s.Append(Entry{Text: "good one"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := appendRaw(path, "{not json\n"); err != nil {
		t.Fatalf("appendRaw: %v", err)
	}
	if err := s.Append(Entry{Text: "good two"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, err := s.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want the 2 valid ones", len(got))
	}
}

func TestAppendIgnoresEmptyText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path}
	if err := s.Append(Entry{Text: "   \n"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got, _ := s.Recent(0)
	if len(got) != 0 {
		t.Fatalf("blank transcript was recorded: %v", got)
	}
}

func TestTrimKeepsNewest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.jsonl")
	s := &Store{Path: path, Max: 3}
	for _, txt := range []string{"a", "b", "c", "d", "e"} {
		if err := s.Append(Entry{Text: txt}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := s.Recent(0)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("kept %d entries, want Max=3", len(got))
	}
	if got[0].Text != "e" || got[2].Text != "c" {
		var texts []string
		for _, e := range got {
			texts = append(texts, e.Text)
		}
		t.Errorf("kept %v, want the newest three", strings.Join(texts, ","))
	}
}

// DefaultPath is XDG-derived, so it has to be pinned or it asserts against the
// developer's real state directory.
func TestDefaultPathHonorsXDGStateHome(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_STATE_HOME", base)

	got, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(base, "mavor", "history.jsonl"); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
