package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/history"
)

// seedHistory writes a log under a temporary XDG_STATE_HOME and returns the
// transcripts in the order `mavor history` will list them: newest first.
func seedHistory(t *testing.T, texts ...string) []string {
	t.Helper()
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)
	store := &history.Store{Path: filepath.Join(state, "mavor", "history.jsonl")}
	for i, text := range texts {
		if err := store.Append(history.Entry{Text: text, At: time.Unix(int64(1700000000+i), 0)}); err != nil {
			t.Fatalf("seed history: %v", err)
		}
	}
	newestFirst := make([]string, 0, len(texts))
	for i := len(texts) - 1; i >= 0; i-- {
		newestFirst = append(newestFirst, texts[i])
	}
	return newestFirst
}

// capturePicked replaces the clipboard write and reports what was copied.
func capturePicked(t *testing.T) *string {
	t.Helper()
	var got string
	prev := clipboardCopy
	clipboardCopy = func(text string) error {
		got = text
		return nil
	}
	t.Cleanup(func() { clipboardCopy = prev })
	return &got
}

// The listing feeds a picker on stdin, so a transcript has to occupy exactly
// one row no matter how many lines the model produced.
func TestHistoryListingIsOneRowPerTranscript(t *testing.T) {
	seedHistory(t, "first one", "second\nspans\nlines")

	out, err := execCLI(t, "history", "--timestamps=false")
	if err != nil {
		t.Fatalf("mavor history: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("listing had %d rows, want 2:\n%s", len(lines), out)
	}
	if lines[0] != "second spans lines" {
		t.Errorf("newest row = %q, want the multi-line transcript flattened", lines[0])
	}
}

// --number is what makes the listing addressable: the number it prints is the
// one `history copy` takes.
func TestHistoryNumbersAreTheIndexCopyTakes(t *testing.T) {
	want := seedHistory(t, "oldest", "middle", "newest")

	out, err := execCLI(t, "history", "--number", "--timestamps=false")
	if err != nil {
		t.Fatalf("mavor history --number: %v", err)
	}
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		wantRow := strings.Join([]string{strconv.Itoa(i), want[i]}, "\t")
		if line != wantRow {
			t.Errorf("row %d = %q, want %q", i, line, wantRow)
		}
	}

	for i, text := range want {
		got := capturePicked(t)
		if err := execCLIErr(t, "history", "copy", strconv.Itoa(i)); err != nil {
			t.Fatalf("mavor history copy %d: %v", i, err)
		}
		if *got != text {
			t.Errorf("history copy %d copied %q, want %q", i, *got, text)
		}
	}
}

// `history copy` with no index recovers the transcript that just vanished,
// which is the case it exists for.
func TestHistoryCopyDefaultsToNewest(t *testing.T) {
	seedHistory(t, "older", "the one that vanished")
	got := capturePicked(t)

	if err := execCLIErr(t, "history", "copy"); err != nil {
		t.Fatalf("mavor history copy: %v", err)
	}
	if *got != "the one that vanished" {
		t.Errorf("copied %q, want the newest transcript", *got)
	}
}

// --pick is the whole rofi round trip in one command. Any dmenu-compatible
// picker echoes the chosen row back, so a shell command standing in for one is
// a faithful test of the contract.
func TestHistoryPickCopiesTheChosenRow(t *testing.T) {
	want := seedHistory(t, "oldest", "middle", "newest")

	for i := range want {
		got := capturePicked(t)
		// sed picks row i the way a user picks it in rofi: by echoing it back.
		picker := "sed -n " + strconv.Itoa(i+1) + "p"
		if err := execCLIErr(t, "history", "--pick", "--picker", picker); err != nil {
			t.Fatalf("mavor history --pick (%s): %v", picker, err)
		}
		if *got != want[i] {
			t.Errorf("picking row %d copied %q, want %q", i, *got, want[i])
		}
	}
}

// The index, not the visible text, identifies the entry — so a picker that
// reformats or truncates what it displays still resolves to the right one.
func TestHistoryPickResolvesByIndexNotText(t *testing.T) {
	seedHistory(t, "older", "the real transcript")
	got := capturePicked(t)

	// A picker that returns the index and mangles everything after it.
	if err := execCLIErr(t, "history", "--pick", "--picker", `sed -n '1s/\t.*/\tmangled/p'`); err != nil {
		t.Fatalf("mavor history --pick: %v", err)
	}
	if *got != "the real transcript" {
		t.Errorf("copied %q, want the transcript the index named", *got)
	}
}

// Escape in a picker exits non-zero with no output. That is the user changing
// their mind, and it must not surface as an error.
func TestHistoryPickTreatsCancellationAsSuccess(t *testing.T) {
	seedHistory(t, "something")
	got := capturePicked(t)

	if err := execCLIErr(t, "history", "--pick", "--picker", "exit 1"); err != nil {
		t.Fatalf("cancelling the picker returned an error: %v", err)
	}
	if *got != "" {
		t.Errorf("cancelling copied %q, want nothing copied", *got)
	}
}

// $MAVOR_PICKER configures the picker once, instead of in every keybind.
func TestPickerResolutionOrder(t *testing.T) {
	t.Setenv(pickerEnv, "from-env")
	if got := pickerCommand("from-flag"); got != "from-flag" {
		t.Errorf("with a flag set, picker = %q, want the flag to win", got)
	}
	if got := pickerCommand(""); got != "from-env" {
		t.Errorf("with no flag, picker = %q, want $%s", got, pickerEnv)
	}
	os.Unsetenv(pickerEnv)
	if got := pickerCommand(""); got != defaultPicker {
		t.Errorf("with neither set, picker = %q, want %q", got, defaultPicker)
	}
}

func TestParsePickedIndex(t *testing.T) {
	tests := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"0\thello world", 0, false},
		{"12\t2026-09-09T11:00:00-04:00\ttext", 12, false},
		{"3\n", 3, false},
		{"", 0, true},
		{"no index here", 0, true},
	}
	for _, tc := range tests {
		got, err := parsePickedIndex(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parsePickedIndex(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("parsePickedIndex(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// --null is for the pickers that read NUL-separated input, so a transcript can
// never be split across rows by something in its own text.
func TestHistoryNullSeparator(t *testing.T) {
	seedHistory(t, "one", "two")

	out, err := execCLI(t, "history", "--null", "--timestamps=false")
	if err != nil {
		t.Fatalf("mavor history --null: %v", err)
	}
	if strings.Contains(out, "\n") {
		t.Errorf("--null output contains a newline: %q", out)
	}
	if got := bytes.Count([]byte(out), []byte{0}); got != 2 {
		t.Errorf("--null wrote %d NUL separators, want 2", got)
	}
}

// A picker row is for choosing between transcripts, and a full RFC3339 stamp
// pushes the text that distinguishes them off to the right. --pick drops the
// column unless it is asked for.
func TestHistoryPickOmitsTimestampsByDefault(t *testing.T) {
	seedHistory(t, "older", "the newest transcript")
	capturePicked(t)

	menu := pickerMenu(t, "history", "--pick")
	if strings.Contains(menu, "2023-") {
		t.Errorf("picker menu carried a timestamp:\n%s", menu)
	}
	if !strings.Contains(menu, "0\tthe newest transcript") {
		t.Errorf("picker menu = %q, want a bare numbered row", menu)
	}
}

// Knowing whether a transcript is from ten minutes ago or yesterday can be what
// tells two similar ones apart, so the column is still available on request.
func TestHistoryPickKeepsTimestampsWhenAsked(t *testing.T) {
	seedHistory(t, "older", "the newest transcript")
	capturePicked(t)

	menu := pickerMenu(t, "history", "--pick", "--timestamps")
	if !strings.Contains(menu, "2023-") {
		t.Errorf("--timestamps did not restore the column:\n%s", menu)
	}
}

// pickerMenu runs the command with a picker that records what it was shown
// before choosing the first row. The menu never reaches stdout — it is written
// to the picker's stdin — so intercepting it is the only way to assert on the
// rows a user would actually see in rofi.
func pickerMenu(t *testing.T, args ...string) string {
	t.Helper()
	shown := filepath.Join(t.TempDir(), "menu")
	if err := execCLIErr(t, append(args, "--picker", "tee "+shown+" | head -1")...); err != nil {
		t.Fatalf("mavor %s: %v", strings.Join(args, " "), err)
	}
	menu, err := os.ReadFile(shown)
	if err != nil {
		t.Fatalf("read captured menu: %v", err)
	}
	return string(menu)
}

// The plain listing keeps its timestamps, which is what it did before --pick
// existed and what a human reading the log wants.
func TestHistoryListingKeepsTimestampsByDefault(t *testing.T) {
	seedHistory(t, "something")

	out, err := execCLI(t, "history")
	if err != nil {
		t.Fatalf("mavor history: %v", err)
	}
	if !strings.Contains(out, "2023-") {
		t.Errorf("listing lost its timestamp column:\n%s", out)
	}
	out, err = execCLI(t, "history", "--timestamps=false")
	if err != nil {
		t.Fatalf("mavor history --timestamps=false: %v", err)
	}
	if strings.Contains(out, "2023-") {
		t.Errorf("--timestamps=false still printed a timestamp:\n%s", out)
	}
}
