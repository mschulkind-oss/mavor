package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// The daemon's first line is the only place the log can say which build is
// running. A pid and a start time say when the process began, and `mavor
// version` asks whichever binary is on PATH now — which, after an install, is
// not the one the daemon is running. Mistaking a three-hour-old process for a
// freshly built one is exactly what this line exists to prevent, so all three
// identity fields have to be in it.
func TestDaemonStartLogNamesTheBuild(t *testing.T) {
	Version, Commit, BuildTime = "v9.9.9", "v9.9.9-3-gabc1234-dirty", "2026-11-18T22:08:45Z"
	t.Cleanup(func() { Version, Commit, BuildTime = "dev", "unknown", "unknown" })

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := config.Default()
	cfg.Model = "whisper-tiny.en"
	logDaemonStart(logger, cfg)

	got := buf.String()
	for _, want := range []string{
		"daemon starting",
		"version=v9.9.9",
		"commit=v9.9.9-3-gabc1234-dirty",
		"built=2026-11-18T22:08:45Z",
		"model=whisper-tiny.en",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("startup line is missing %q; log:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "\n"); n != 1 {
		t.Errorf("startup wrote %d lines, want 1; log:\n%s", n, got)
	}
}
