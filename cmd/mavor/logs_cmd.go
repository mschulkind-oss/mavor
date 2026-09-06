package main

import (
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

func runLogs(args []string) error {
	follow := false
	lines := 50
	var customFile string

	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-f" || a == "--follow":
			follow = true
		case (a == "-n" || a == "--lines") && i+1 < len(args):
			if n, err := strconv.Atoi(args[i+1]); err == nil && n > 0 {
				lines = n
			}
			i++
		case a == "--file" && i+1 < len(args):
			customFile = args[i+1]
			i++
		case a == "-h" || a == "--help":
			fmt.Println(`usage: mavor logs [-f|--follow] [-n <lines>] [--file <path>]

View and follow real-time logs from the mavor dictation daemon.

options:
  -f, --follow       follow log output in real time (stream new entries)
  -n, --lines <N>    number of past lines to show (default: 50)
  --file <path>      read directly from a specific log file instead of journald`)
			return nil
		}
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
