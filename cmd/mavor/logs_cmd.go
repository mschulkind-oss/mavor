package main

import (
	"github.com/spf13/cobra"

	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
)

func newLogsCmd() *cobra.Command {
	var (
		follow     bool
		lines      int
		customFile string
	)
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "view or stream daemon logs",
		Long: "View and follow real-time logs from the mavor dictation daemon.\n\n" +
			"Reads from journald when it is available and the daemon log file otherwise.",
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runLogs(follow, lines, customFile)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output in real time")
	cmd.Flags().IntVarP(&lines, "lines", "n", 50, "number of past lines to show")
	cmd.Flags().StringVar(&customFile, "file", "", "read from this log file instead of journald")
	return cmd
}

func runLogs(follow bool, lines int, customFile string) error {
	if lines <= 0 {
		return fmt.Errorf("--lines must be positive, got %d", lines)
	}

	// Try journalctl first if no explicit file was requested
	if customFile == "" {
		if _, err := exec.LookPath("journalctl"); err == nil {
			jArgs := []string{"--user", "-u", "mavor", "-n", strconv.Itoa(lines), "--no-pager"}
			if follow {
				jArgs = append(jArgs, "-f")
			}
			cmd := exec.Command("journalctl", jArgs...)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			if err := cmd.Run(); err == nil {
				return nil
			}
			// If journalctl returns non-zero (e.g. no systemd unit logs yet), fall back to log file
		}
	}

	// Fall back to daemon.log file
	logPath := customFile
	if logPath == "" {
		cfg, err := config.Load("")
		if err == nil && cfg.Paths.Log != "" {
			logPath = cfg.Paths.Log
		} else {
			stateHome := os.Getenv("XDG_STATE_HOME")
			if stateHome == "" {
				home, _ := os.UserHomeDir()
				stateHome = filepath.Join(home, ".local", "state")
			}
			logPath = filepath.Join(stateHome, "mavor", "daemon.log")
		}
	}

	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("open log file %s: %w (is the daemon running?)", logPath, err)
	}
	defer f.Close()

	fmt.Printf("📖 Reading logs from %s\n\n", logPath)

	// Read last N lines
	scanner := bufio.NewScanner(f)
	var allLines []string
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	startIdx := 0
	if len(allLines) > lines {
		startIdx = len(allLines) - lines
	}
	for _, l := range allLines[startIdx:] {
		fmt.Println(l)
	}

	if !follow {
		return nil
	}

	// Follow new lines
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return err
		}
		fmt.Print(line)
	}
}
