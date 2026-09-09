package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/mschulkind-oss/mavor/internal/history"
	"github.com/mschulkind-oss/mavor/internal/output"
)

// newHistoryCmd lists past transcripts so text that never landed can be
// recovered. The default listing is one transcript per line, newest first,
// which is the shape a picker like rofi, wofi, fuzzel or dmenu expects on stdin.
func newHistoryCmd() *cobra.Command {
	var (
		limit    int
		asJSON   bool
		copyTo   bool
		index    int
		noStamps bool
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "list past transcripts, newest first, or recover one",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runHistory(cmd.OutOrStdout(), limit, asJSON, copyTo, index, noStamps)
		},
	}
	f := cmd.Flags()
	f.IntVarP(&limit, "limit", "n", 20, "maximum entries to show (0 for all)")
	f.BoolVar(&asJSON, "json", false, "emit JSON lines including timestamps")
	f.BoolVar(&copyTo, "copy", false, "copy an entry to the clipboard instead of listing")
	f.IntVar(&index, "index", 0, "with --copy, which entry to copy (0 = newest)")
	f.BoolVar(&noStamps, "no-timestamps", false, "omit the leading timestamp column")
	return cmd
}

func runHistory(w io.Writer, limit int, asJSON, copyTo bool, index int, noStamps bool) error {
	store, err := history.New()
	if err != nil {
		return err
	}
	entries, err := store.Recent(limit)
	if err != nil {
		return err
	}

	if copyTo {
		if index < 0 || index >= len(entries) {
			return fmt.Errorf("no history entry at index %d (have %d)", index, len(entries))
		}
		text := entries[index].Text
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

	enc := json.NewEncoder(w)
	for _, e := range entries {
		switch {
		case asJSON:
			if err := enc.Encode(e); err != nil {
				return fmt.Errorf("encode entry: %w", err)
			}
		case noStamps:
			fmt.Fprintln(w, oneLine(e.Text))
		default:
			fmt.Fprintf(w, "%s\t%s\n", e.At.Local().Format(time.RFC3339), oneLine(e.Text))
		}
	}
	return nil
}

// oneLine flattens a transcript so each entry stays a single selectable row in
// a picker. Newlines become spaces rather than being dropped.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
