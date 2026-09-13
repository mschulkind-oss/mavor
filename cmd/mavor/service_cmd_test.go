package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The unit file is the one thing mavor writes that outlives the process and is
// run again, unattended, at every login. These tests are about containment:
// where that write lands, and that it lands nowhere else.

func TestServicePathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/xdg-config")

	want := filepath.Join("/xdg-config", "systemd", "user", "mavor.service")
	if got := getServicePath(); got != want {
		t.Errorf("getServicePath() = %q, want %q", got, want)
	}
}

func TestServicePathFallsBackToHomeConfig(t *testing.T) {
	// systemd reads ~/.config/systemd/user only when XDG_CONFIG_HOME is
	// unset, so an empty value has to mean "unset" here too.
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "/home/nobody")

	want := filepath.Join("/home/nobody", ".config", "systemd", "user", "mavor.service")
	if got := getServicePath(); got != want {
		t.Errorf("getServicePath() = %q, want %q", got, want)
	}
}

// TestServiceInstallStaysInsideConfigHome is the regression test for a unit
// test that rewrote a developer's real mavor.service.
//
// `mavor setup` installs the systemd unit as its last step. getServicePath
// resolved that path from $HOME while the tests around it isolated only
// XDG_CONFIG_HOME, so the write escaped the sandbox and landed on the real
// unit — with an ExecStart naming the `go test` binary under /tmp/go-build*,
// which systemd then failed to exec at every login once that directory was
// cleaned up.
//
// The assertion that matters is the second one: nothing may appear under HOME.
func TestServiceInstallStaysInsideConfigHome(t *testing.T) {
	configHome := t.TempDir()
	home := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	t.Setenv("HOME", home)

	// systemctl is absent in some environments (containers) and present in
	// others; either way the unit file is written before it is consulted, and
	// a failure to reload or enable is reported as a note rather than an
	// error. So this covers both.
	if err := runServiceInstall(false); err != nil {
		t.Fatalf("runServiceInstall() error = %v", err)
	}

	unit := filepath.Join(configHome, "systemd", "user", "mavor.service")
	content, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("unit was not written to the configured config home %s: %v", configHome, err)
	}
	if !strings.Contains(string(content), "ExecStart=") {
		t.Errorf("unit at %s has no ExecStart line:\n%s", unit, content)
	}

	if entries, err := os.ReadDir(home); err == nil && len(entries) > 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("service install wrote outside the configured config home, into HOME=%s: %v", home, names)
	}
}
