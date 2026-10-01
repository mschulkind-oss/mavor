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
)

func TestGNOMEStorybook(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "x11_harness.py", binary, "storybook")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	result, err := cmd.CombinedOutput()
	t.Log(string(result))
	if err != nil {
		t.Fatal(err)
	}
}

func TestStorybookRegressions(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "test_storybook")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func TestStorybookMissingPrerequisite(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "x11_harness.py", binary, "storybook")
	cmd.Env = append(os.Environ(), "MAVOR_GNOME_SHELL=/nonexistent/mavor-storybook-shell")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "mavor-storybook-shell") || !strings.Contains(string(out), "Supervisor reaped private worker group and adopted holders") {
		t.Fatalf("missing prerequisite/cleanup not verified: %v: %s", err, out)
	}
}
