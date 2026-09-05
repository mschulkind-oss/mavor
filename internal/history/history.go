// Package history records completed transcriptions so a user can recover text
// that never reached its destination. Synthetic typing can be swallowed by a
// window that lost focus, a compositor that dropped the keystrokes, or an app
// that was still starting — and the transcript is gone with no way back.
//
// Architecture and invariants: docs/reference/how-mavor-works.md
package history

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DefaultMax is how many transcripts are kept before the oldest are dropped.
const DefaultMax = 500

// Entry is one completed transcription.
type Entry struct {
	At   time.Time `json:"at"`
	Text string    `json:"text"`
}

// Store is an append-only JSONL log of transcripts. JSONL keeps appends atomic
// enough for a single writer while surviving arbitrary text — newlines and tabs
// in a transcript are escaped by the encoder, so one record is always one line.
type Store struct {
	// Path is the log file. Parent directories are created on first append.
	Path string
	// Max caps retained entries; zero means DefaultMax.
	Max int

	mu sync.Mutex
}

// DefaultPath returns the history log location, honouring XDG_STATE_HOME.
func DefaultPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "mavor", "history.jsonl"), nil
}

// New returns a Store at the default path.
func New() (*Store, error) {
	path, err := DefaultPath()
	if err != nil {
		return nil, err
	}
	return &Store{Path: path}, nil
}

// Append records one transcript. Blank text is ignored: an empty transcript is
// nothing to recover. Callers may ignore the error — failing to log must never
// cost the user their dictation.
func (s *Store) Append(e Entry) error {
	if strings.TrimSpace(e.Text) == "" {
		return nil
	}
	if e.At.IsZero() {
		e.At = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode history entry: %w", err)
	}
	if err := appendRaw(s.Path, string(line)+"\n"); err != nil {
		return err
	}
	return s.trimLocked()
}

// Recent returns entries newest first, at most limit of them (0 for all).
// A missing log is the normal first-run state and yields no entries.
func (s *Store) Recent(limit int) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recentLocked(limit)
}

func (s *Store) recentLocked(limit int) ([]Entry, error) {
	entries, err := s.readAllLocked()
	if err != nil {
		return nil, err
	}
	// Newest first: recovery starts from the transcript that just vanished.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	if limit > 0 && len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

// readAllLocked returns entries oldest first, skipping unparseable lines: a
// truncated write must not cost the user the rest of their history.
func (s *Store) readAllLocked() ([]Entry, error) {
	f, err := os.Open(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open history: %w", err)
	}
	defer f.Close()

	var entries []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return entries, nil
}

// trimLocked drops the oldest entries once the log exceeds Max, rewriting via a
// temp file so an interrupted trim cannot truncate the history.
func (s *Store) trimLocked() error {
	max := s.Max
	if max <= 0 {
		max = DefaultMax
	}
	entries, err := s.readAllLocked()
	if err != nil {
		return err
	}
	if len(entries) <= max {
		return nil
	}
	entries = entries[len(entries)-max:]

	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".history-*.jsonl")
	if err != nil {
		return fmt.Errorf("create temp history: %w", err)
	}
	defer os.Remove(tmp.Name())

	w := bufio.NewWriter(tmp)
	for _, e := range entries {
		line, err := json.Marshal(e)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("encode history entry: %w", err)
		}
		if _, err := w.WriteString(string(line) + "\n"); err != nil {
			tmp.Close()
			return fmt.Errorf("write temp history: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("flush temp history: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp history: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return fmt.Errorf("chmod temp history: %w", err)
	}
	if err := os.Rename(tmp.Name(), s.Path); err != nil {
		return fmt.Errorf("replace history: %w", err)
	}
	return nil
}

func appendRaw(path, line string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open history for append: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		return fmt.Errorf("append history: %w", err)
	}
	return nil
}
