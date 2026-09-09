package main

import (
	"bytes"
	"strings"
	"testing"
)

// execCLI runs the real command tree, so what these tests exercise is the
// parser a user types at, not a hand-rolled call to the function behind it.
// A fresh root per call keeps flag state from leaking between tests.
func execCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	err := root.Execute()
	return out.String(), err
}

// The old hand-rolled parsers accepted "-n 1" but rejected "-n1", because Go's
// stdlib flag package looks up a flag literally named "n1". Every other Unix
// tool with an -n takes the attached form, so this is the regression the switch
// to pflag was for.
func TestShortFlagsTakeAttachedValues(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	for _, form := range []string{"-n1", "-n=1", "-n", "--limit=1"} {
		args := []string{"history", form}
		if form == "-n" {
			args = append(args, "1")
		}
		t.Run(strings.Join(args[1:], " "), func(t *testing.T) {
			if _, err := execCLI(t, args...); err != nil {
				t.Errorf("mavor %s: %v", strings.Join(args, " "), err)
			}
		})
	}
}

// Combined short flags are the other thing getopt callers expect and stdlib
// flag never offered.
func TestCombinedShortFlagsParse(t *testing.T) {
	out, err := execCLI(t, "models", "list", "-iv")
	if err != nil {
		t.Fatalf("mavor models list -iv: %v (out: %s)", err, out)
	}
}

// A mistyped flag has to fail loudly. The hand-rolled loops silently ignored
// anything they did not recognise, so `mavor daemon --lgo-file /tmp/x` started
// a daemon that logged somewhere else entirely.
func TestUnknownFlagIsRejected(t *testing.T) {
	for _, args := range [][]string{
		{"history", "--nope"},
		{"logs", "--nope"},
		{"config", "init", "--nope"},
		{"service", "install", "--nope"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			if _, err := execCLI(t, args...); err == nil {
				t.Errorf("mavor %s: want an error for the unknown flag, got none", strings.Join(args, " "))
			}
		})
	}
}

// An unknown subcommand names itself rather than falling through to the
// parent's default behaviour.
func TestUnknownSubcommandIsRejected(t *testing.T) {
	_, err := execCLI(t, "config", "wat")
	if err == nil {
		t.Fatal("mavor config wat: want an error, got none")
	}
	if !strings.Contains(err.Error(), "wat") {
		t.Errorf("error = %q, want it to name the unknown subcommand", err)
	}
}

// Help lists every command, so `mavor help` stays the map of the tool.
func TestRootHelpListsEveryCommand(t *testing.T) {
	out, err := execCLI(t, "--help")
	if err != nil {
		t.Fatalf("mavor --help: %v", err)
	}
	for _, cmd := range []string{
		"daemon", "toggle", "start", "stop", "status", "doctor",
		"setup", "config", "service", "models", "logs", "history", "version",
	} {
		if !strings.Contains(out, cmd) {
			t.Errorf("help output missing command %q", cmd)
		}
	}
}

// Cobra generates per-command help from the flags themselves, which is the
// thing the hand-written usage blocks kept drifting from.
func TestSubcommandHelpNamesItsOwnFlags(t *testing.T) {
	out, err := execCLI(t, "history", "--help")
	if err != nil {
		t.Fatalf("mavor history --help: %v", err)
	}
	for _, want := range []string{"--limit", "--copy", "--index", "--no-timestamps", "--json"} {
		if !strings.Contains(out, want) {
			t.Errorf("history help missing %q", want)
		}
	}
}

// execCLIErr is execCLI for the callers that only care whether the command
// succeeded.
func execCLIErr(t *testing.T, args ...string) error {
	t.Helper()
	_, err := execCLI(t, args...)
	return err
}
