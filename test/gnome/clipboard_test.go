//go:build gnome

package gnome

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/output"
)

// TestDispatcherChild is also the executable used by the isolated harness.
// It runs production code with real wl-copy, not a test Runner.
func TestDispatcherChild(t *testing.T) {
	if os.Getenv("MAVOR_GNOME_CHILD") != "1" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := output.NewClipboard().Emit(ctx, os.Getenv("MAVOR_GNOME_TEXT")); err != nil {
		t.Fatal(err)
	}
}

func TestGNOMEClipboard(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"focused", "overview", "deadline", "forced"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "harness.py", binary, scenario)
			// Graceful cancellation leaves the supervisor alive to kill and reap
			// the isolated worker group. Parent death follows the same path.
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
			cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
			cmd.WaitDelay = 10 * time.Second
			result, err := cmd.CombinedOutput()
			t.Log(string(result))
			if scenario == "forced" {
				if err == nil || !strings.Contains(string(result), "Native transfer complete; forcing worker SIGKILL") || !strings.Contains(string(result), "Supervisor reaped private worker group and adopted holders") {
					t.Fatalf("forced-death cleanup not verified: %v", err)
				}
			} else if err != nil {
				t.Fatalf("real GNOME acceptance failed: %v", err)
			}
		})
	}
}

// Lifecycle tests require only Python/Linux, not GNOME or a selection substitute.
func TestHarnessCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := exec.CommandContext(ctx, "python3", "-m", "unittest", "test_supervise").CombinedOutput()
	t.Log(string(result))
	if err != nil {
		t.Fatal(err)
	}
}
