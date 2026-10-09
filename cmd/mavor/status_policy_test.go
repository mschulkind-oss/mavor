package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/ipc"
)

// `mavor status` prints the derived state first and the live dictations stacked
// beneath it, recording row first, with provenance kept on stderr.
func TestStatusRendersStateAndStackOnStdout(t *testing.T) {
	var out, diag bytes.Buffer
	resp := ipc.Response{
		State:        "recording",
		Recording:    true,
		Transcribing: 2,
		Source:       "companion-backup",
		Warning:      "GPU-enabled server request failed",
	}
	if err := writeStatus(&out, &diag, resp); err != nil {
		t.Fatal(err)
	}
	want := "recording\n  - recording\n  - transcribing\n  - transcribing\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
	if !strings.Contains(diag.String(), "companion-backup") || !strings.Contains(diag.String(), "GPU-enabled server request failed") {
		t.Fatalf("stderr = %q, want provenance", diag.String())
	}
}

// Idle with nothing in flight is just the state word: no empty stack lines.
func TestStatusIdleHasNoStackRows(t *testing.T) {
	var out, diag bytes.Buffer
	if err := writeStatus(&out, &diag, ipc.Response{State: "idle"}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "idle\n" {
		t.Fatalf("stdout = %q, want %q", out.String(), "idle\n")
	}
}

// Beyond the three visible rows the rest collapse to a count.
func TestStatusStackCollapsesBeyondThreeRows(t *testing.T) {
	var out, diag bytes.Buffer
	if err := writeStatus(&out, &diag, ipc.Response{State: "recording", Recording: true, Transcribing: 5}); err != nil {
		t.Fatal(err)
	}
	want := "recording\n  - recording\n  - transcribing\n  - transcribing\n  - ... and 3 more\n"
	if out.String() != want {
		t.Fatalf("stdout = %q, want %q", out.String(), want)
	}
}

func TestStatusJSONSerializesWholeResponse(t *testing.T) {
	var buf bytes.Buffer
	resp := ipc.Response{State: "transcribing", Transcribing: 1, Source: "main"}
	if err := json.NewEncoder(&buf).Encode(resp); err != nil {
		t.Fatal(err)
	}
	var back ipc.Response
	if err := json.Unmarshal(buf.Bytes(), &back); err != nil {
		t.Fatal(err)
	}
	if back.State != "transcribing" || back.Transcribing != 1 || back.Source != "main" {
		t.Fatalf("round trip = %+v", back)
	}
}
