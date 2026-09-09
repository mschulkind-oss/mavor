package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
)

// defaultPicker is the dmenu-compatible command `--pick` runs when neither
// --picker nor $MAVOR_PICKER says otherwise. Anything that reads lines on
// stdin and writes the chosen one to stdout works here — rofi, wofi, fuzzel,
// dmenu, fzf — which is why the flag takes a shell command rather than a
// picker name.
const defaultPicker = "rofi -dmenu -i -p transcript"

// pickerEnv overrides defaultPicker without a flag, so a user configures their
// picker once in their shell profile instead of in every keybind.
const pickerEnv = "MAVOR_PICKER"

type historyOpts struct {
	limit      int
	asJSON     bool
	number     bool
	timestamps bool
	pick       bool
	picker     string
	nul        bool
}

func newHistoryCmd() *cobra.Command {
	var o historyOpts

	cmd := &cobra.Command{
		Use:   "history",
		Short: "list past transcripts, newest first, or recover one",
		Long: `List past transcripts, newest first.

The default listing is one transcript per line — the shape a picker like rofi,
wofi, fuzzel or dmenu expects on stdin. Rows are TAB-separated, and each column
is opt-in so the listing can be shaped for whatever is reading it:

  --number       prefix each row with its index, the number 'history copy'
                 and 'history --pick' both take
  --timestamps   the leading timestamp column; on when listing, off under
                 --pick, where a full stamp pushes the text that actually
                 distinguishes two transcripts off to the right
  --json         JSON Lines including timestamps, for scripts

--pick does the whole round trip in one command: it renders a numbered listing,
feeds it to the picker, and copies whatever was chosen to the clipboard. That is
the form to bind to a key.`,
		Example: `  # Bind this to a key — no shell plumbing needed.
  mavor history --pick

  # Use a different picker, or set MAVOR_PICKER once in your profile.
  mavor history --pick --picker 'fuzzel --dmenu'

  # Recover the newest transcript without a picker at all.
  mavor history copy

  # Keep the timestamps in the picker after all.
  mavor history --pick --timestamps

  # The listing by hand, piped wherever you like.
  mavor history -n0 --timestamps=false`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A picker row is for telling transcripts apart, and a full RFC3339
			// stamp is 25 columns of prefix that never does that. Drop it under
			// --pick unless this run asked for it by name.
			if o.pick && !cmd.Flags().Changed("timestamps") {
				o.timestamps = false
			}
			if o.pick {
				return runHistoryPick(cmd.OutOrStdout(), o)
			}
			return runHistoryList(cmd.OutOrStdout(), o)
		},
	}

	f := cmd.Flags()
	f.IntVarP(&o.limit, "limit", "n", 20, "maximum entries to show (0 for all)")
	f.BoolVar(&o.asJSON, "json", false, "emit JSON Lines including timestamps")
	f.BoolVar(&o.number, "number", false, "prefix each row with its index")
	f.BoolVar(&o.timestamps, "timestamps", true, "include the timestamp column (default false under --pick)")
	f.BoolVarP(&o.pick, "pick", "p", false, "run a picker and copy the chosen transcript")
	f.StringVar(&o.picker, "picker", "", "picker command for --pick (default $"+pickerEnv+", else "+strconv.Quote(defaultPicker)+")")
	f.BoolVar(&o.nul, "null", false, "separate rows with NUL instead of newline")

	cmd.AddCommand(newHistoryCopyCmd())
	return cmd
}

func newHistoryCopyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "copy [index]",
		Short: "copy one transcript to the clipboard (default: the newest)",
		Long: "Copy one transcript to the clipboard without typing it, so recovery does\n" +
			"not inject keystrokes into whatever window happens to be focused.\n\n" +
			"The index is the one 'mavor history --number' prints: 0 is the newest.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			index := 0
			if len(args) == 1 {
				n, err := strconv.Atoi(args[0])
				if err != nil {
					return fmt.Errorf("history copy: %q is not an index: %w", args[0], err)
				}
				index = n
			}
			return runHistoryCopy(cmd.ErrOrStderr(), index)
		},
	}
}

// recentEntries reads the log, honouring limit as `mavor history -n` means it.
func recentEntries(limit int) ([]history.Entry, error) {
	store, err := history.New()
	if err != nil {
		return nil, err
	}
	return store.Recent(limit)
}

// renderHistory writes the listing. It is separate from the command so tests
// can read the exact bytes a picker would be handed.
func renderHistory(w io.Writer, entries []history.Entry, o historyOpts) error {
	sep := "\n"
	if o.nul {
		sep = "\x00"
	}
	enc := json.NewEncoder(w)
	for i, e := range entries {
		if o.asJSON {
			// JSON Lines is a defined format; --null and --number would break
			// the contract a consumer relies on, so they do not apply here.
			if err := enc.Encode(e); err != nil {
				return fmt.Errorf("encode entry: %w", err)
			}
			continue
		}
		var row strings.Builder
		if o.number {
			fmt.Fprintf(&row, "%d\t", i)
		}
		if o.timestamps {
			fmt.Fprintf(&row, "%s\t", e.At.Local().Format(time.RFC3339))
		}
		row.WriteString(oneLine(e.Text))
		if _, err := io.WriteString(w, row.String()+sep); err != nil {
			return err
		}
	}
	return nil
}

func runHistoryList(w io.Writer, o historyOpts) error {
	entries, err := recentEntries(o.limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "no transcripts recorded yet")
		return nil
	}
	return renderHistory(w, entries, o)
}

// clipboardCopy is the clipboard write, indirected so a test can see which
// transcript a picker selection resolved to without a compositor to copy into.
var clipboardCopy = func(text string) error {
	return output.NewWayland().CopyOnly(context.Background(), text)
}

func runHistoryCopy(msg io.Writer, index int) error {
	entries, err := recentEntries(0)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(entries) {
		return fmt.Errorf("no history entry at index %d (have %d)", index, len(entries))
	}
	text := entries[index].Text
	if err := clipboardCopy(text); err != nil {
		return fmt.Errorf("copy to clipboard: %w", err)
	}
	fmt.Fprintf(msg, "copied %d characters to the clipboard\n", len(text))
	return nil
}

// pickerCommand resolves which picker --pick should run: the flag, then
// $MAVOR_PICKER, then the rofi default.
func pickerCommand(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv(pickerEnv); env != "" {
		return env
	}
	return defaultPicker
}

// runHistoryPick renders a numbered listing, hands it to the picker, and copies
// whatever came back.
//
// The rows carry their index as the first TAB-separated field and the choice is
// read back off that field, which is what keeps this picker-agnostic: rofi's
// `-format i` would do the same job, but wofi, fuzzel and dmenu have no
// equivalent, and echoing the line back is the one thing every dmenu clone does.
func runHistoryPick(msg io.Writer, o historyOpts) error {
	entries, err := recentEntries(o.limit)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		return fmt.Errorf("no transcripts recorded yet")
	}

	var menu bytes.Buffer
	listing := o
	listing.number = true
	listing.asJSON = false
	listing.nul = false
	if err := renderHistory(&menu, entries, listing); err != nil {
		return err
	}

	command := pickerCommand(o.picker)
	// sh -c, so the flag holds a command line the user would type — the picker
	// plus its own options — rather than a bare binary name.
	cmd := exec.Command("sh", "-c", command)
	cmd.Stdin = &menu
	cmd.Stderr = os.Stderr
	chosen, err := cmd.Output()
	if err != nil {
		// A picker cancelled with Escape exits non-zero with no output. That is
		// the user changing their mind, not a failure to report.
		if len(bytes.TrimSpace(chosen)) == 0 {
			return nil
		}
		return fmt.Errorf("picker %q: %w", command, err)
	}

	index, err := parsePickedIndex(string(chosen))
	if err != nil {
		return err
	}
	if index < 0 || index >= len(entries) {
		return fmt.Errorf("picker returned index %d, which is outside the %d entries it was given", index, len(entries))
	}
	return runHistoryCopy(msg, index)
}

// parsePickedIndex reads the leading index field off the row a picker echoed
// back. Everything after the first TAB is the transcript and is ignored: the
// index is what identifies the entry, so a picker that trims or reformats the
// visible text still resolves to the right transcript.
func parsePickedIndex(line string) (int, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return 0, fmt.Errorf("picker returned nothing")
	}
	field, _, _ := strings.Cut(line, "\t")
	index, err := strconv.Atoi(strings.TrimSpace(field))
	if err != nil {
		return 0, fmt.Errorf("picker returned %q, which does not start with an index: %w", line, err)
	}
	return index, nil
}

// oneLine flattens a transcript so each entry stays a single selectable row in
// a picker. Newlines become spaces rather than being dropped.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
