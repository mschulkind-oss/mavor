//go:build integration || e2e

package integration

import (
	"fmt"
	"github.com/mschulkind-oss/mavor/test/desktop"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// MavorBinary is the absolute path to the built `mavor` binary. Tests pass it to
// Harness.RunDaemon so each test reuses the same fresh build.
var MavorBinary string

func TestMain(m *testing.M) {
	desktop.GPUFailureChild() // Controlled server child must not recursively rebuild the CLI.
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
	os.Exit(m.Run())
}
