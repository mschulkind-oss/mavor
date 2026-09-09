package main

import (
	"github.com/spf13/cobra"

	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const systemdUnitTemplate = `[Unit]
Description=mavor voice dictation daemon
Documentation=https://github.com/mschulkind-oss/mavor
PartOf=graphical-session.target
After=pipewire.service wireplumber.service

[Service]
Type=simple
ExecStart=%s daemon
# Also how an upgrade lands: the daemon exits 75 once it is idle and notices
# its own binary has been replaced, and this line starts the new one.
Restart=on-failure
RestartSec=2s
Environment=PULSE_LATENCY_MSEC=30
PassEnvironment=WAYLAND_DISPLAY XDG_CURRENT_DESKTOP

[Install]
WantedBy=graphical-session.target
`

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "manage the systemd user service (mavor.service)",
		Args:  cobra.NoArgs,
		// Bare `mavor service` reports status, as it did before.
		RunE: func(_ *cobra.Command, _ []string) error { return runServiceStatus() },
	}

	var start bool
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "install and enable the systemd user service",
		Args:  cobra.NoArgs,
		RunE:  func(_ *cobra.Command, _ []string) error { return runServiceInstall(start) },
	}
	installCmd.Flags().BoolVarP(&start, "start", "s", false, "start the service once it is installed")

	action := func(use, short, verb string) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE:  func(_ *cobra.Command, _ []string) error { return runServiceAction(verb) },
		}
	}

	cmd.AddCommand(
		installCmd,
		&cobra.Command{
			Use:   "status",
			Short: "show systemd user service status",
			Args:  cobra.NoArgs,
			RunE:  func(_ *cobra.Command, _ []string) error { return runServiceStatus() },
		},
		action("start", "start the mavor background service", "start"),
		action("stop", "stop the mavor background service", "stop"),
		action("restart", "restart the mavor background service", "restart"),
		&cobra.Command{
			Use:   "uninstall",
			Short: "disable and remove the systemd user service",
			Args:  cobra.NoArgs,
			RunE:  func(_ *cobra.Command, _ []string) error { return runServiceUninstall() },
		},
		&cobra.Command{
			Use:   "show",
			Short: "print the systemd service unit template",
			Args:  cobra.NoArgs,
			RunE:  func(_ *cobra.Command, _ []string) error { return runServiceShow() },
		},
	)
	return cmd
}

func getServicePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".config", "systemd", "user", "mavor.service")
}

func runServiceInstall(start bool) error {
	unitPath := getServicePath()
	binPath := getBinaryPath()

	unitDir := filepath.Dir(unitPath)
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		return fmt.Errorf("create unit directory %s: %w", unitDir, err)
	}

	content := fmt.Sprintf(systemdUnitTemplate, binPath)
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("write unit file %s: %w", unitPath, err)
	}
	fmt.Printf("✅ Installed systemd user unit at %s (ExecStart=%s)\n", unitPath, binPath)

	if err := runCmd("systemctl", "--user", "daemon-reload"); err != nil {
		fmt.Printf("⚠️  Note: systemctl --user daemon-reload skipped or failed: %v\n", err)
	}
	if err := runCmd("systemctl", "--user", "enable", "mavor"); err != nil {
		fmt.Printf("⚠️  Note: systemctl --user enable mavor skipped or failed: %v\n", err)
	} else {
		fmt.Println("✅ Enabled mavor.service for graphical session startup")
	}

	if start {
		if err := runCmd("systemctl", "--user", "restart", "mavor"); err != nil {
			fmt.Printf("⚠️  Note: systemctl --user restart mavor skipped or failed: %v\n", err)
		} else {
			fmt.Println("✅ Started mavor.service")
		}
	}
	return nil
}

func runServiceStatus() error {
	unitPath := getServicePath()
	if _, err := os.Stat(unitPath); os.IsNotExist(err) {
		fmt.Printf("❌ Service unit not found at %s (run 'mavor service install' to install)\n", unitPath)
		return nil
	}
	fmt.Printf("Unit file: %s\n\n", unitPath)
	cmd := exec.Command("systemctl", "--user", "status", "mavor")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	_ = cmd.Run()
	return nil
}

func runServiceAction(action string) error {
	return runCmd("systemctl", "--user", action, "mavor")
}

func runServiceUninstall() error {
	unitPath := getServicePath()
	_ = runCmd("systemctl", "--user", "disable", "--now", "mavor")
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove unit file %s: %w", unitPath, err)
	}
	_ = runCmd("systemctl", "--user", "daemon-reload")
	fmt.Printf("✅ Uninstalled systemd user service (%s)\n", unitPath)
	return nil
}

func runServiceShow() error {
	binPath := getBinaryPath()
	fmt.Printf(systemdUnitTemplate, binPath)
	return nil
}

func runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
