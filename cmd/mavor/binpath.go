package main

import (
	"os"
	"path/filepath"
	"strings"
)

// getBinaryPath is the path written into the unit's ExecStart, and the path
// the daemon watches for an upgrade. Both need the same thing from it: a name
// for this program that still names this program after the next `brew
// upgrade` or `just install`.
//
// os.Executable is not that name. On Linux it reads /proc/self/exe, which the
// kernel has already resolved past every symlink, so a Homebrew install
// answers with the versioned Cellar path — the one path an upgrade is
// guaranteed to remove. filepath.EvalSymlinks is here for the case where
// os.Executable did not resolve (it is a no-op when it did), and
// stableBinaryPath undoes the resolution Homebrew cannot live with.
func getBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".local", "bin", "mavor")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return stableBinaryPath(exe)
}

// cellarSegment is the directory Homebrew installs versioned kegs under:
// <prefix>/Cellar/<formula>/<version>/bin/mavor.
const cellarSegment = "/Cellar/"

// stableBinaryPath rewrites a Homebrew Cellar path to the matching `opt`
// path — <prefix>/Cellar/mavor/0.2.0/bin/mavor becomes
// <prefix>/opt/mavor/bin/mavor — and returns every other path unchanged.
//
// Homebrew retargets the opt symlink at the new keg on every upgrade and
// deletes the old versioned directory on the next `brew cleanup`, so a unit
// file holding a Cellar path fails at the next login with status 203/EXEC.
// This is the rule `brew services` follows by writing `opt_bin` into the unit
// it generates for a formula.
func stableBinaryPath(p string) string {
	idx := strings.Index(p, cellarSegment)
	if idx < 0 {
		return p
	}
	formula, afterFormula, ok := strings.Cut(p[idx+len(cellarSegment):], "/")
	if !ok || formula == "" {
		return p
	}
	// Drop the version segment; keep whatever the keg put under it.
	_, tail, ok := strings.Cut(afterFormula, "/")
	if !ok || tail == "" {
		return p
	}
	return filepath.Join(p[:idx], "opt", formula, tail)
}

// restartOnUpgrade reports whether the daemon should exit when the binary
// underneath it is replaced. Exiting only counts as an upgrade if something
// starts the new binary afterwards, so this is on under systemd — which sets
// INVOCATION_ID for every unit it runs — and off everywhere else, where the
// same exit would just end dictation with nobody to notice.
// MAVOR_RESTART_ON_UPGRADE overrides the guess in either direction.
func restartOnUpgrade() bool {
	switch strings.ToLower(os.Getenv("MAVOR_RESTART_ON_UPGRADE")) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	return os.Getenv("INVOCATION_ID") != ""
}
