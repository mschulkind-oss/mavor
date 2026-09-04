package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageOutput(t *testing.T) {
	buf := new(bytes.Buffer)
	usage(buf)
	out := buf.String()
	for _, cmd := range []string{"daemon", "toggle", "start", "stop", "status", "doctor", "config", "service", "models", "version"} {
		if !strings.Contains(out, cmd) {
			t.Errorf("usage output missing command %q", cmd)
		}
	}
}

func TestVersionOutput(t *testing.T) {
	if err := runVersion(); err != nil {
		t.Fatalf("runVersion() error = %v", err)
	}
}

// Several clones of this repo install to the same ~/.local/bin/mavor, so the
// version line has to name the commit it was built from — otherwise there is
// no way to tell which tree produced the running binary.
func TestVersionStringIdentifiesTheBuild(t *testing.T) {
	Commit = "abc1234-dirty"
	BuildTime = "2026-08-31T12:00:00Z"
	t.Cleanup(func() { Commit, BuildTime = "unknown", "unknown" })

	got := versionString()
	for _, want := range []string{Version, Commit, BuildTime, BuildTags} {
		if !strings.Contains(got, want) {
			t.Errorf("versionString() = %q, missing %q", got, want)
		}
	}
}

func TestConfigCommands(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	// 1. Config path
	if err := runConfig([]string{"path"}); err != nil {
		t.Fatalf("runConfig(path) error = %v", err)
	}

	// 2. Config init
	if err := runConfig([]string{"init"}); err != nil {
		t.Fatalf("runConfig(init) error = %v", err)
	}

	// Verify file was written
	cfgPath := filepath.Join(tmpDir, "mavor", "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file was not created at %s: %v", cfgPath, err)
	}

	// 3. Config init duplicate without force fails
	if err := runConfig([]string{"init"}); err == nil {
		t.Fatalf("expected error re-initializing without --force, got nil")
	}

	// 4. Config init with force succeeds
	if err := runConfig([]string{"init", "--force"}); err != nil {
		t.Fatalf("runConfig(init --force) error = %v", err)
	}

	// 5. Config show
	if err := runConfig([]string{"show"}); err != nil {
		t.Fatalf("runConfig(show) error = %v", err)
	}
}

func TestServiceShow(t *testing.T) {
	if err := runServiceShow(); err != nil {
		t.Fatalf("runServiceShow() error = %v", err)
	}
}

func TestModelsList(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	// Empty list
	if err := runModels([]string{"list"}); err != nil {
		t.Fatalf("runModels(list) error = %v", err)
	}

	// Create fake model files
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	if err := os.MkdirAll(modelDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), []byte("test"), 0o644)

	// List with model
	if err := runModels([]string{"list"}); err != nil {
		t.Fatalf("runModels(list) with model error = %v", err)
	}
}

func TestDoctorRuns(t *testing.T) {
	// Doctor may return non-zero in test environment (e.g. no physical display), but should execute cleanly without panic
	_ = runDoctor(nil)
}

func TestSetupCommand(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)
	t.Setenv("XDG_CACHE_HOME", tmpDir)

	// Create fake base model file so setup doesn't make real network requests in unit test
	modelDir := filepath.Join(tmpDir, "mavor", "models")
	_ = os.MkdirAll(modelDir, 0o755)
	_ = os.WriteFile(filepath.Join(modelDir, "ggml-base.en.bin"), []byte("test-model"), 0o644)

	if err := runSetup(nil); err != nil {
		t.Fatalf("runSetup() error = %v", err)
	}

	cfgPath := filepath.Join(tmpDir, "mavor", "config.toml")
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config file was not created by setup at %s: %v", cfgPath, err)
	}
}

func TestLogsCommand(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "daemon.log")
	_ = os.WriteFile(logFile, []byte("time=2026-08-16 level=INFO msg=\"daemon started\"\n"), 0o644)

	if err := runLogs([]string{"--file", logFile, "-n", "10"}); err != nil {
		t.Fatalf("runLogs() error = %v", err)
	}
}
