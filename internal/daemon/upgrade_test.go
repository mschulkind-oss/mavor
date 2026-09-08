package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// installBinary writes a stand-in for the mavor binary, replacing rather than
// rewriting any file already there — which is what every real installer does,
// and what the watch keys on.
func installBinary(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".new"
	if err := os.WriteFile(tmp, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatalf("rename %s -> %s: %v", tmp, path, err)
	}
}

func newUpgradeDaemon(t *testing.T, binPath string) (*Daemon, string) {
	t.Helper()
	return newTestDaemon(t, func(c *Config) {
		c.BinaryPath = binPath
		c.UpgradeCheckInterval = 5 * time.Millisecond
	})
}

// runDaemonErr is runDaemon's sibling for the tests that care what Run
// returned rather than only that it stopped.
func runDaemonErr(t *testing.T, d *Daemon) (done <-chan error, cancel func()) {
	t.Helper()
	ctx, cancelFn := context.WithCancel(t.Context())
	ch := make(chan error, 1)
	go func() { ch <- d.Run(ctx) }()
	return ch, cancelFn
}

func TestUpgradeWatchExitsWhenBinaryReplaced(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mavor")
	installBinary(t, bin, "old")
	d, sock := newUpgradeDaemon(t, bin)
	done, cancel := runDaemonErr(t, d)
	defer cancel()
	waitForState(t, sock, "idle")

	installBinary(t, bin, "new")

	select {
	case err := <-done:
		if !errors.Is(err, ErrBinaryReplaced) {
			t.Fatalf("Run() = %v, want ErrBinaryReplaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit after its binary was replaced")
	}
}

func TestUpgradeWatchIgnoresAnUnchangedBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mavor")
	installBinary(t, bin, "old")
	d, sock := newUpgradeDaemon(t, bin)
	done, cancel := runDaemonErr(t, d)
	defer cancel()
	waitForState(t, sock, "idle")

	select {
	case err := <-done:
		t.Fatalf("daemon exited on its own: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if got := sendWithRetry(t, sock, "status").State; got != "idle" {
		t.Fatalf("state = %q, want idle", got)
	}
}

func TestUpgradeWatchWaitsForIdle(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mavor")
	installBinary(t, bin, "old")
	d, sock := newUpgradeDaemon(t, bin)
	done, cancel := runDaemonErr(t, d)
	defer cancel()
	waitForState(t, sock, "idle")

	sendWithRetry(t, sock, "start")
	waitForState(t, sock, "recording")
	installBinary(t, bin, "new")

	// Many watch ticks pass while recording; none of them may end the daemon.
	select {
	case err := <-done:
		t.Fatalf("daemon exited mid-recording: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Stopping runs the mock transcription and returns to idle, and the
	// upgrade lands on the next tick after that.
	sendWithRetry(t, sock, "stop")
	select {
	case err := <-done:
		if !errors.Is(err, ErrBinaryReplaced) {
			t.Fatalf("Run() = %v, want ErrBinaryReplaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit once it went idle")
	}
}

func TestUpgradeWatchToleratesAMissingBinary(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "mavor")
	installBinary(t, bin, "old")
	d, sock := newUpgradeDaemon(t, bin)
	done, cancel := runDaemonErr(t, d)
	defer cancel()
	waitForState(t, sock, "idle")

	// The gap an installer opens between removing and writing the file is not
	// a new version, and must not be read as one.
	if err := os.Remove(bin); err != nil {
		t.Fatalf("remove %s: %v", bin, err)
	}
	select {
	case err := <-done:
		t.Fatalf("daemon exited while its binary was missing: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	installBinary(t, bin, "new")
	select {
	case err := <-done:
		if !errors.Is(err, ErrBinaryReplaced) {
			t.Fatalf("Run() = %v, want ErrBinaryReplaced", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit once the new binary appeared")
	}
}

func TestUpgradeWatchOffWithoutABinaryPath(t *testing.T) {
	d, sock := newTestDaemon(t)
	if d.binaryPath != "" {
		t.Fatalf("binaryPath = %q, want empty by default", d.binaryPath)
	}
	done, cancel := runDaemonErr(t, d)
	defer cancel()
	waitForState(t, sock, "idle")
	select {
	case err := <-done:
		t.Fatalf("daemon exited on its own: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestUpgradeIntervalDefaults(t *testing.T) {
	d := New(Config{BinaryPath: "/nonexistent"})
	if d.upgradeInterval != defaultUpgradeInterval {
		t.Errorf("upgradeInterval = %v, want %v", d.upgradeInterval, defaultUpgradeInterval)
	}
}
