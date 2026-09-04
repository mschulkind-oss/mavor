//go:build integration || e2e

package integration

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// Tests that build an overlay in-process share one compositor, because GTK
// imposes two constraints that a per-test compositor cannot satisfy together:
//
//  1. GTK can be initialized once per process. A binary that runs a second
//     gtk.Application takes a SIGTERM seconds later.
//  2. A GTK main loop cannot outlive the compositor it connected to. When sway
//     goes away underneath it, the process dies the same way.
//
// One application per process and one compositor per process is the only
// combination that satisfies both. Tests that drive the daemon as a
// subprocess are unaffected and keep using Start for their own compositor.
var (
	sharedOnce      sync.Once
	sharedHarness   *Harness
	sharedRuntime   string
	sharedStartFail error
)

// sharedCompositor returns the process-wide compositor, starting it on first
// use. It is configured for the most demanding caller — 1920x1080 with waybar
// — since a test that does not need waybar is not harmed by its presence.
func sharedCompositor(t *testing.T) *Harness {
	t.Helper()

	sharedOnce.Do(func() {
		dir, err := os.MkdirTemp("", "mavor-shared-compositor-*")
		if err != nil {
			sharedStartFail = fmt.Errorf("shared compositor tempdir: %w", err)
			return
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			sharedStartFail = fmt.Errorf("shared compositor tempdir mode: %w", err)
			return
		}
		sharedRuntime = dir
		sharedHarness = start(t, Options{Width: 1920, Height: 1080, LaunchWaybar: true}, dir)
	})

	if sharedStartFail != nil {
		t.Fatal(sharedStartFail)
	}
	sharedHarness.adopt(t)
	return sharedHarness
}

// stopSharedCompositor tears the shared compositor down. Called from TestMain
// after every test has finished, never from a test's cleanup.
func stopSharedCompositor() {
	if sharedHarness != nil {
		// Detach first: sway and waybar write on the way down, and every
		// test has finished, so there is no live T to log into.
		sharedHarness.detach()
		sharedHarness.Stop()
	}
	if sharedRuntime != "" {
		_ = os.RemoveAll(sharedRuntime)
	}
}
