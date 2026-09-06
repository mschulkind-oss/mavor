package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVerboseIsOffByDefault(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if Default().Logging.Verbose {
		t.Error("Logging.Verbose defaults on; the preview alone logs on a 30ms tick")
	}
}

func TestVerboseIsReadFromTheFile(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("[logging]\nverbose = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cfg.Logging.Verbose {
		t.Error("logging.verbose = true in the file did not reach the config")
	}
}

// The whole table is optional; a file without it must not be an unknown-key
// warning or a parse failure.
func TestConfigWithoutALoggingTableIsFine(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(`model = "whisper-tiny.en"`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if len(f.UnknownKeys) != 0 {
		t.Errorf("UnknownKeys = %v, want none", f.UnknownKeys)
	}
	if f.Logging.Verbose {
		t.Error("absent table should leave Verbose false")
	}
}
