//go:build gnome

package gnome

import (
	"context"
	"encoding/json"
	"github.com/mschulkind-oss/mavor/test/desktop"
	"os"
	"os/exec"
	"path/filepath"
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
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.json")
	manifest := filepath.Join(dir, "manifest.json")
	raw, err := json.Marshal(desktop.Scenes())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(catalog, raw, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "hud_storybook_harness.py", binary, catalog, manifest)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	result, err := cmd.CombinedOutput()
	t.Log(string(result))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var evidence []desktop.Evidence
	if err = json.Unmarshal(raw, &evidence); err != nil {
		t.Fatal(err)
	}
	extraRaw, err := os.ReadFile(manifest + ".hidden.json")
	if err != nil {
		t.Fatal(err)
	}
	var extra desktop.Evidence
	if err = json.Unmarshal(extraRaw, &extra); err != nil {
		t.Fatal(err)
	}
	if err = desktop.ValidateArtifact(evidence[0].File, extra); err != nil {
		t.Fatal(err)
	}
	if err = desktop.BuildReport("../reports", "gnome-storybook", "GNOME Shell / Mutter", "Shell Screenshot · real production XWayland HUD", "Native GNOME panel + production margin 8px", evidence); err != nil {
		t.Fatal(err)
	}
}

func TestStorybookRegressions(t *testing.T) {
	cmd := exec.Command("python3", "-m", "unittest", "test_storybook", "test_clipboard_qa")
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
	catalog := filepath.Join(t.TempDir(), "catalog.json")
	if err = os.WriteFile(catalog, []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "hud_storybook_harness.py", binary, catalog, filepath.Join(t.TempDir(), "manifest.json"))
	cmd.Env = append(os.Environ(), "MAVOR_GNOME_SHELL=/nonexistent/mavor-storybook-shell")
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "mavor-storybook-shell") || !strings.Contains(string(out), "Supervisor reaped private worker group and adopted holders") {
		t.Fatalf("missing prerequisite/cleanup not verified: %v: %s", err, out)
	}
}

func TestGNOMEClipboardQAReport(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "x11_harness.py", binary, "clipboard-qa")
	out, err := cmd.CombinedOutput()
	t.Log(string(out))
	if err != nil {
		t.Fatal(err)
	}
}
