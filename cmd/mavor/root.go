package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/mschulkind-oss/mavor/internal/daemon"
)

// keybindingExample is appended to the root help. A dictation daemon is
// useless until it is bound to a key, so the one thing a new user must do next
// belongs in the help they see first.
const keybindingExample = `Keybinding example (sway; adapt for your compositor):
  exec mavor daemon
  bindsym $mod+grave exec mavor toggle`

// newRootCmd assembles the whole command tree. It is a constructor rather than
// a package-level variable so each test gets a command with fresh flag values —
// cobra flags keep their state across Execute calls, and a shared root would
// leak --force from one test into the next.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "mavor",
		Short: "low-latency voice dictation for Wayland",
		Long: "mavor — low-latency voice dictation for Wayland\n\n" +
			"Transcription is entirely local: no cloud API, no account, no telemetry.",
		Example:      keybindingExample,
		Version:      versionString(),
		SilenceUsage: true,
		// Errors are printed by main, which also decides the exit code.
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	// -v for --version at the root only. `mavor daemon -v` is verbose, which is
	// a different flag on a different command, so the two never collide.
	root.Flags().BoolP("version", "v", false, "print the version and exit")

	// Groups preserve the sections the hand-written help had. Cobra sorts
	// commands alphabetically otherwise, which buries `setup` — the one command
	// a new user needs — between `service` and `start`.
	root.AddGroup(
		&cobra.Group{ID: groupSetup, Title: "First-Run & Setup:"},
		&cobra.Group{ID: groupCore, Title: "Core Commands:"},
		&cobra.Group{ID: groupEnv, Title: "Environment & Service Management:"},
	)

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add(groupSetup, newSetupCmd(), newDoctorCmd())
	add(groupCore,
		newDaemonCmd(), newToggleCmd(), newStartCmd(), newStopCmd(),
		newStatusCmd(), newLogsCmd(), newHistoryCmd(),
	)
	add(groupEnv, newConfigCmd(), newServiceCmd(), newModelsCmd(), newVersionCmd())
	return root
}

// Help sections, in the order they are printed.
const (
	groupSetup = "setup"
	groupCore  = "core"
	groupEnv   = "env"
)

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "show version and build tags",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runVersion()
		},
	}
}

// exitUpgraded is what `mavor daemon` returns when it stepped aside for a
// newly installed binary. It is deliberately a failure code: that is what
// makes the unit's Restart=on-failure start the new version, and EX_TEMPFAIL
// is the closest thing sysexits.h has to "nothing is wrong, try again".
const exitUpgraded = 75

func exit(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	if errors.Is(err, daemon.ErrBinaryReplaced) {
		os.Exit(exitUpgraded)
	}
	os.Exit(1)
}
