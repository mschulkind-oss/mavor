package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// A model download is hundreds of megabytes over a link mavor does not
// control, and it used to print one line and then nothing at all. On a slow
// connection that is several minutes of a program that looks hung, and the
// only way to tell a download from a stall was to watch the directory grow.

// progressReader wraps a download and reports how it is going.
//
// It reports on a timer rather than per Read: a 429 MB body arrives in tens of
// thousands of reads, and redrawing on each one costs more than the download.
type progressReader struct {
	inner io.Reader
	total int64 // 0 when the server sends no Content-Length

	read    atomic.Int64
	started time.Time
	done    chan struct{}
	// interactive decides between redrawing one line and printing a new one.
	// Resolved once at construction so Close formats the same way report did.
	interactive bool
}

func newProgressReader(r io.Reader, total int64, label string) *progressReader {
	p := &progressReader{
		inner: r, total: total, started: time.Now(),
		done:        make(chan struct{}),
		interactive: isCharDevice(os.Stderr),
	}
	go p.report(label)
	return p
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.inner.Read(b)
	p.read.Add(int64(n))
	return n, err
}

// Close stops the reporting goroutine and leaves a final line behind.
func (p *progressReader) Close() {
	close(p.done)
	// One completed line that survives, since the in-place updates are
	// overwritten and a scrollback with no record of the download is worse
	// than one extra line.
	// Clear the redrawn line first, but only where one was being redrawn:
	// the padding is invisible on a terminal and is 78 stray spaces in a log.
	if p.interactive {
		fmt.Fprintf(os.Stderr, "\r%s\r", strings.Repeat(" ", 78))
	}
	fmt.Fprintf(os.Stderr, "  downloaded %s in %s\n",
		formatFileSize(p.read.Load()), time.Since(p.started).Round(time.Second))
}

func (p *progressReader) report(label string) {
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	// In-place updates only where something is watching. Piped to a file or a
	// CI log, carriage returns turn the record into one unreadable line, so
	// there it prints a new line every few seconds instead.
	every := 1
	if !p.interactive {
		every = 10 // ~5s between lines
	}
	for n := 1; ; n++ {
		select {
		case <-p.done:
			return
		case <-tick.C:
			if n%every != 0 {
				continue
			}
			line := p.line(label)
			if p.interactive {
				fmt.Fprintf(os.Stderr, "\r%-78s", line)
			} else {
				fmt.Fprintln(os.Stderr, "  "+line)
			}
		}
	}
}

func (p *progressReader) line(label string) string {
	got := p.read.Load()
	elapsed := time.Since(p.started)
	var rate string
	if s := elapsed.Seconds(); s > 0 {
		rate = fmt.Sprintf(" at %s/s", formatFileSize(int64(float64(got)/s)))
	}

	// Without Content-Length there is no percentage and no estimate to give,
	// and inventing one is worse than saying how much has arrived.
	if p.total <= 0 {
		return fmt.Sprintf("  %s: %s%s", label, formatFileSize(got), rate)
	}

	pct := float64(got) / float64(p.total) * 100
	const width = 24
	filled := int(pct / 100 * width)
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)

	var eta string
	if got > 0 && got < p.total {
		remaining := time.Duration(float64(elapsed) / float64(got) * float64(p.total-got))
		eta = fmt.Sprintf(", %s left", remaining.Round(time.Second))
	}
	return fmt.Sprintf("  %s %5.1f%% of %s%s%s", bar, pct, formatFileSize(p.total), rate, eta)
}

// isCharDevice reports whether w is a terminal, without pulling in
// golang.org/x/term for one boolean. A pipe or a file is not a character
// device, which is exactly the distinction that matters here.
func isCharDevice(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
