package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/output"
)

func TestClipboardSelection(t *testing.T) {
	cfg := config.Default()
	cfg.Output.Driver = "clipboard"
	d, close, err := selectOutput(cfg.Output, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := d.(*output.Clipboard); !ok || close != nil {
		t.Fatalf("dispatcher = %T, close present = %v", d, close != nil)
	}
	cfg.Output.Driver = "typo"
	if _, _, err := selectOutput(cfg.Output, nil); err == nil {
		t.Fatal("unknown driver selected")
	}
	cfg.Output.Driver = "paste"
	d, _, err = selectOutput(cfg.Output, nil)
	if _, ok := d.(*output.Paste); !ok || err != nil {
		t.Fatalf("paste changed: %T %v", d, err)
	}
}

func TestClipboardToolRequirements(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[output]\ndriver = \"clipboard\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Output.Driver = "clipboard"
	for _, name := range []string{"parec", "whisper-cli", "wl-copy"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if missing := getMissingTools(cfg); len(missing) != 0 {
		t.Fatalf("missing = %v", missing)
	}
	for _, check := range []func() (bool, string){checkWtype, checkClipboard, checkOutput} {
		if ok, msg := check(); !ok {
			t.Fatal(msg)
		}
	}
	ok, msg := outputVerdict(cfg, false, true, false, false, "")
	if !ok || !strings.Contains(msg, "paste manually") || !strings.Contains(msg, "does not verify") {
		t.Fatalf("verdict = %v %s", ok, msg)
	}
	if err := os.Remove(filepath.Join(dir, "wl-copy")); err != nil {
		t.Fatal(err)
	}
	if ok, _ := checkClipboard(); ok {
		t.Fatal("missing wl-copy accepted")
	}
	if ok, _ := checkOutput(); ok {
		t.Fatal("missing wl-copy accepted by output check")
	}
	if missing := getMissingTools(cfg); len(missing) != 1 || missing[0] != "wl-copy" {
		t.Fatalf("missing = %v", missing)
	}
	cfg.Output.Driver = "typo"
	if ok, _ := outputVerdict(cfg, true, true, true, true, ""); ok {
		t.Fatal("unknown driver accepted")
	}
}

func TestX11ClipboardSelectionAndRequirements(t *testing.T) {
	cfg := config.Default()
	cfg.Output.Driver = "clipboard"
	cfg.Output.ClipboardBackend = "x11"
	d, close, err := selectOutput(cfg.Output, nil)
	if _, ok := d.(*output.X11Clipboard); !ok || err != nil || close == nil {
		t.Fatalf("selection %T %v", d, err)
	}
	if err := close(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[output]\ndriver = \"clipboard\"\nclipboard_backend = \"x11\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parec", "whisper-cli", "xclip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if missing := getMissingTools(cfg); len(missing) != 0 {
		t.Fatalf("missing %v", missing)
	}
	t.Setenv("DISPLAY", "")
	if ok, _ := checkOutput(); ok {
		t.Fatal("missing DISPLAY accepted")
	}
	t.Setenv("DISPLAY", ":99")
	t.Setenv("XAUTHORITY", filepath.Join(dir, "auth"))
	if ok, _ := checkClipboard(); ok {
		t.Fatal("missing auth accepted")
	}
	if err := os.WriteFile(os.Getenv("XAUTHORITY"), []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, check := range []func() (bool, string){checkOutput, checkClipboard} {
		if ok, msg := check(); !ok {
			t.Fatal(msg)
		}
	}
	if err := os.Remove(filepath.Join(dir, "xclip")); err != nil {
		t.Fatal(err)
	}
	if missing := getMissingTools(cfg); len(missing) != 1 || missing[0] != "xclip" {
		t.Fatalf("missing %v", missing)
	}
	if ok, _ := checkOutput(); ok {
		t.Fatal("missing xclip accepted")
	}
}

func TestX11SetupRequiresSessionEnvironment(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	t.Setenv("DISPLAY", "")
	if err := os.MkdirAll(filepath.Dir(config.Path()), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config.Path(), []byte("[output]\ndriver = \"clipboard\"\nclipboard_backend = \"x11\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"parec", "whisper-cli", "xclip"} {
		if err := os.WriteFile(filepath.Join(os.Getenv("PATH"), name), []byte("#!/bin/sh\nexit 99\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := runSetup(false); err == nil || !strings.Contains(err.Error(), "DISPLAY") {
		t.Fatalf("setup error: %v", err)
	}
}
