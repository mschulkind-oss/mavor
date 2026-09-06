package main

import (
	"strings"
	"testing"
	"time"
)

func atProgress(total, got int64, elapsed time.Duration) *progressReader {
	p := &progressReader{total: total, started: time.Now().Add(-elapsed)}
	p.read.Store(got)
	return p
}

// The line a user watches for several minutes has to say how far along it is,
// how big the thing is, and when it will end.
func TestProgressLineReportsShareSizeRateAndETA(t *testing.T) {
	const total = 429 * 1024 * 1024
	line := atProgress(total, total/4, 10*time.Second).line("downloading")

	for _, want := range []string{"25.0%", "429.0 MB", "/s", "left"} {
		if !strings.Contains(line, want) {
			t.Errorf("line %q is missing %q", line, want)
		}
	}
}

// A server that sends no Content-Length leaves nothing to compute a share or
// an estimate from, and inventing either is worse than saying how much has
// arrived.
func TestProgressLineWithoutAContentLength(t *testing.T) {
	line := atProgress(0, 5*1024*1024, 2*time.Second).line("downloading")

	if strings.Contains(line, "%") || strings.Contains(line, "left") {
		t.Errorf("line %q claims a share or an estimate with no total to base it on", line)
	}
	if !strings.Contains(line, "5.0 MB") {
		t.Errorf("line %q does not say how much has arrived", line)
	}
}

// The bar is drawn from a percentage, so it must not run past its width when a
// server sends more bytes than it promised.
func TestProgressBarStaysWithinItsWidthWhenOversized(t *testing.T) {
	line := atProgress(100, 250, time.Second).line("downloading")
	if n := strings.Count(line, "█"); n > 24 {
		t.Errorf("bar drew %d filled cells, over its 24-cell width: %q", n, line)
	}
}

// Zero bytes in zero time is the first tick of every download, and must not
// divide by zero or estimate anything.
func TestProgressLineAtTheVeryStart(t *testing.T) {
	line := atProgress(1000, 0, 0).line("downloading")
	if line == "" {
		t.Error("no line at the start of a download")
	}
	if strings.Contains(line, "left") {
		t.Errorf("line %q estimates a finish from no progress at all", line)
	}
}
