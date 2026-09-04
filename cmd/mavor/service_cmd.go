package main

import (
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
Restart=on-failure
RestartSec=2s
Environment=PULSE_LATENCY_MSEC=30
PassEnvironment=WAYLAND_DISPLAY XDG_CURRENT_DESKTOP

[Install]
WantedBy=graphical-session.target
`

func runService(args []string) error {
	if len(args) == 0 {
		return runServiceStatus()
	}
	switch args[0] {
	case "install":
		start := false
		for _, a := range args[1:] {
			if a == "--start" || a == "-s" {
				start = true
			}
		}
		return runServiceInstall(start)
	case "status":
		return runServiceStatus()
	case "start":
		return runServiceAction("start")
	case "stop":
		return runServiceAction("stop")
	case "restart":
		return runServiceAction("restart")
	case "uninstall":
		return runServiceUninstall()
	case "show":
		return runServiceShow()
	case "help", "-h", "--help":
		fmt.Println(`usage: mavor service <command>

commands:
  install [--start]   install and enable systemd user service (~/.config/systemd/user/mavor.service)
  status              show systemd user service status
  start               start the mavor background service
  stop                stop the mavor background service
  restart             restart the mavor background service
  uninstall           disable and remove the systemd user service
  show                print the systemd service unit template`)
		return nil
	default:
		return fmt.Errorf("unknown service command: %s (try 'mavor service help')", args[0])
	}
}

func getServicePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/root"
	}
	return filepath.Join(home, ".config", "systemd", "user", "mavor.service")
}

func getBinaryPath() string {
	exe, err := os.Executable()
	if err == nil {
		if resolved, err := filepath.EvalSymlinks(exe); err == nil {
			return resolved
		}
		return exe
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "bin", "mavor")
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
