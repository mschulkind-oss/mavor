package main

import (
	"bytes"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"strings"
	"testing"
)

func TestStatusKeepsStateStdoutAndReportsBackupProvenance(t *testing.T) {
	var out, diag bytes.Buffer
	err := writeStatus(&out, &diag, ipc.Response{State: "idle", Source: "companion-backup", Warning: "GPU-enabled server request failed"})
	if err != nil || out.String() != "idle\n" || !strings.Contains(diag.String(), "companion-backup") || !strings.Contains(diag.String(), "GPU-enabled server request failed") {
		t.Fatalf("%q %q %v", out.String(), diag.String(), err)
	}
}
