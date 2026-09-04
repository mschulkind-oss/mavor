//go:build integration || e2e

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

// MavorBinary is the absolute path to the built `mavor` binary. Tests pass it to
// Harness.RunDaemon so each test reuses the same fresh build.
var MavorBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "mavor-integration-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "tempdir:", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)
	MavorBinary = filepath.Join(dir, "mavor")

	wd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	build := exec.Command("go", "build", "-o", MavorBinary, "./cmd/mavor")
	build.Dir = repoRoot
	build.Stdout = os.Stderr
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "build:", err)
		os.Exit(2)
	}
	code := m.Run()
	// Order matters. The GTK application has to stop before the compositor
	// it is connected to: a main loop whose compositor disappears takes the
	// process down with it, which would turn a passing run into a
	// "signal: terminated" failure after every test had already passed.
	overlay.Shutdown()
	stopSharedCompositor()
	os.Exit(code)
}
