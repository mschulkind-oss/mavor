package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
)

// runHistory lists past transcripts so text that never landed can be recovered.
// The default listing is one transcript per line, newest first, which is the
// shape a picker like rofi, wofi, fuzzel or dmenu expects on stdin.
func runHistory(args []string) error {
	fs := flag.NewFlagSet("history", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		limit  = fs.Int("n", 20, "maximum entries to show (0 for all)")
		asJSON = fs.Bool("json", false, "emit JSON lines including timestamps")
		copyTo = fs.Bool("copy", false, "copy the newest entry to the clipboard instead of listing")
		index  = fs.Int("index", 0, "with --copy, which entry to copy (0 = newest)")
		plain  = fs.Bool("no-timestamps", false, "omit the leading timestamp column")
	)
	if err := fs.Parse(args); err != nil {
		return fmt.Errorf("mavor history: %w", err)
	}

	store, err := history.New()
	if err != nil {
		return err
	}
	entries, err := store.Recent(*limit)
	if err != nil {
		return err
	}

	if *copyTo {
		if *index < 0 || *index >= len(entries) {
			return fmt.Errorf("no history entry at index %d (have %d)", *index, len(entries))
		}
		text := entries[*index].Text
		if err := output.NewWayland().CopyOnly(context.Background(), text); err != nil {
			return fmt.Errorf("copy to clipboard: %w", err)
		}
		fmt.Fprintf(os.Stderr, "copied %d characters to the clipboard\n", len(text))
		return nil
	}

	if len(entries) == 0 {
		fmt.Fprintln(os.Stderr, "no transcripts recorded yet")
		return nil
	}

	enc := json.NewEncoder(os.Stdout)
	for _, e := range entries {
		switch {
		case *asJSON:
			if err := enc.Encode(e); err != nil {
				return fmt.Errorf("encode entry: %w", err)
			}
		case *plain:
			fmt.Println(oneLine(e.Text))
		default:
			fmt.Printf("%s\t%s\n", e.At.Local().Format(time.RFC3339), oneLine(e.Text))
		}
	}
	return nil
}

// oneLine flattens a transcript so each entry stays a single selectable row in
// a picker. Newlines become spaces rather than being dropped.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
