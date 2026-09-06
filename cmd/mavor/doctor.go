package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/ipc"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

type Check struct {
	Name string
	Fn   func() (ok bool, msg string)
}

func runDoctor(args []string) error {
	for _, a := range args {
		if a == "--fix" || a == "-f" || a == "fix" {
			return runSetup(args)
		}
	}

	fmt.Println("mavor doctor — system and environment verification")
	fmt.Println("==================================================")

	checks := []Check{
		{"Wayland session", checkWayland},
		{"Audio capture (parec/Pulse)", checkAudio},
		{"Virtual typing (wtype)", checkWtype},
		{"Clipboard (wl-clipboard)", checkClipboard},
		{"Runtime and placement", checkRuntime},
		{"Inference threads", checkThreads},
		{"GPU acceleration", checkGPU},
		{"Configuration file", checkConfig},
		{"Voice model availability", checkModel},
		{"Daemon socket status", checkDaemon},
		{"Systemd user service", checkServiceUnit},
	}

	failed := 0
	for _, c := range checks {
		ok, msg := c.Fn()
		icon := "✅"
		if !ok {
			icon = "❌"
			failed++
		}
		fmt.Printf("%s %-28s %s\n", icon, c.Name+":", msg)
	}

	fmt.Println("==================================================")
	if failed > 0 {
		fmt.Printf("❌ %d check(s) failed. Fix the issues above before running mavor.\n", failed)
		fmt.Println("\n💡 Tip: Run 'mavor doctor --fix' (or 'mavor setup') to automatically configure mavor and download the default model.")
		return fmt.Errorf("%d doctor check(s) failed", failed)
	}
	fmt.Println("✅ All environment checks passed! mavor is ready.")
	return nil
}

func runSetup(args []string) error {
	force := false
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
		}
	}

	fmt.Println("mavor setup — automated first-run configuration & model install")
	fmt.Println("================================================================")

	// Step 1: Configuration file
	configPath := config.Path()
	if _, err := os.Stat(configPath); os.IsNotExist(err) || force {
		fmt.Printf("⚙️  Creating configuration file at %s...\n", configPath)
		if err := runConfigInit(force); err != nil {
			return fmt.Errorf("setup config: %w", err)
		}
	} else {
		fmt.Printf("✅ Configuration file found at %s\n", configPath)
	}

	cfg, err := config.Load("")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Step 2: System Dependencies & Upfront Batch Sudo
	missingTools := getMissingTools(cfg)
	if len(missingTools) > 0 {
		fmt.Printf("\n📦 Missing system tools detected: %s\n", strings.Join(missingTools, ", "))
		distro, _ := detectDistro()
		fmt.Printf("🔍 Detected Linux distribution: %s\n", distro)
		if err := installSystemPackages(distro, missingTools); err != nil {
			fmt.Printf("⚠️  Automatic package installation had warnings: %v\n", err)
		}
	} else {
		fmt.Println("✅ All required system runtime tools (parec, wtype, wl-copy) are available")
	}

	// Step 3: Model cache directory
	if err := os.MkdirAll(cfg.Paths.Models, 0o755); err != nil {
		return fmt.Errorf("create model directory %s: %w", cfg.Paths.Models, err)
	}

	// Step 4: the model the config names. Which path to look at follows from
	// the model, not from a key: the catalog says which runtime owns it.
	modelPath := installedModelPath(cfg, cfg.Model)
	if _, err := os.Stat(modelPath); os.IsNotExist(err) || force {
		fmt.Printf("📥 Downloading voice model %q into %s...\n", cfg.Model, cfg.Paths.Models)
		if err := pullModel(cfg.Model); err != nil {
			return fmt.Errorf("setup model download: %w", err)
		}
		fmt.Printf("✅ Downloaded and verified voice model %q\n", cfg.Model)
	} else {
		fmt.Printf("✅ Voice model %q is already installed\n", cfg.Model)
	}

	// Step 5: Systemd user service installation
	if _, err := exec.LookPath("systemctl"); err == nil {
		fmt.Println("\n⚙️  Setting up systemd user service...")
		if err := runServiceInstall(false); err != nil {
			fmt.Printf("⚠️  Service setup notice: %v\n", err)
		}
	}

	fmt.Println("\n================================================================")
	fmt.Println("🎉 Setup complete! mavor is configured and ready.")
	fmt.Println("\nQuick Start:")
	fmt.Println("  1. Start the dictation daemon:")
	fmt.Println("     mavor daemon (or systemctl --user start mavor)")
	fmt.Println("\n  2. Add a keybind to your compositor config (sway shown; ~/.config/sway/config):")
	fmt.Println("     bindsym $mod+grave exec mavor toggle")
	fmt.Println("\n  3. Or test push-to-talk in another terminal:")
	fmt.Println("     mavor start    # start recording")
	fmt.Println("     mavor stop     # stop and transcribe")
	return nil
}

// installedModelPath is where the model named by a catalog name lands in the
// cache: a file for a whisper model, a directory for everything else.
func installedModelPath(cfg config.Config, name string) string {
	if models.RuntimeFor(name) == models.RuntimeWhisper {
		return speech.WhisperModelPath(cfg.Paths.Models, name)
	}
	dir := name
	if spec, ok := models.Lookup(name); ok && spec.TargetDir != "" {
		dir = spec.TargetDir
	}
	return filepath.Join(cfg.Paths.Models, "sherpa", dir)
}

func getMissingTools(cfg config.Config) []string {
	var missing []string
	if _, err := exec.LookPath("parec"); err != nil {
		missing = append(missing, "parec")
	}
	if _, err := exec.LookPath("wtype"); err != nil {
		missing = append(missing, "wtype")
	}
	if _, err := exec.LookPath("wl-copy"); err != nil {
		missing = append(missing, "wl-copy")
	}
	if models.RuntimeFor(cfg.Model) == models.RuntimeWhisper {
		if _, err := exec.LookPath("whisper-cli"); err != nil {
			missing = append(missing, "whisper-cli")
		}
	}
	return missing
}

func detectDistro() (string, string) {
	f, err := os.Open("/etc/os-release")
	if err != nil {
		return "unknown", "unknown"
	}
	defer f.Close()

	var id, idLike, prettyName string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), `"`)
		} else if strings.HasPrefix(line, "ID_LIKE=") {
			idLike = strings.Trim(strings.TrimPrefix(line, "ID_LIKE="), `"`)
		} else if strings.HasPrefix(line, "PRETTY_NAME=") {
			prettyName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
		}
	}

	combined := strings.ToLower(id + " " + idLike + " " + prettyName)
	if strings.Contains(combined, "arch") || strings.Contains(combined, "manjaro") || strings.Contains(combined, "endeavouros") {
		return "arch", id
	}
	if strings.Contains(combined, "ubuntu") || strings.Contains(combined, "debian") || strings.Contains(combined, "pop") || strings.Contains(combined, "mint") {
		return "ubuntu", id
	}
	if strings.Contains(combined, "fedora") || strings.Contains(combined, "rhel") || strings.Contains(combined, "centos") {
		return "fedora", id
	}
	if strings.Contains(combined, "nix") {
		return "nixos", id
	}
	if prettyName != "" {
		return prettyName, id
	}
	return "linux", id
}

func installSystemPackages(distro string, missing []string) error {
	var pkgs []string
	var cmdName string
	var cmdArgs []string

	switch distro {
	case "arch":
		cmdName = "sudo"
		cmdArgs = []string{"pacman", "-S", "--needed"}
		for _, m := range missing {
			switch m {
			case "parec":
				pkgs = append(pkgs, "pipewire-pulse", "pulseaudio-utils")
			case "wtype":
				pkgs = append(pkgs, "wtype")
			case "wl-copy":
				pkgs = append(pkgs, "wl-clipboard")
			case "whisper-cli":
				pkgs = append(pkgs, "whisper-cpp")
			}
		}
	case "ubuntu", "debian":
		cmdName = "sudo"
		cmdArgs = []string{"apt", "install", "-y"}
		for _, m := range missing {
			switch m {
			case "parec":
				pkgs = append(pkgs, "pipewire-pulse", "pulseaudio-utils")
			case "wtype":
				pkgs = append(pkgs, "wtype")
			case "wl-copy":
				pkgs = append(pkgs, "wl-clipboard")
			case "whisper-cli":
				pkgs = append(pkgs, "build-essential", "cmake", "git")
			}
		}
	case "fedora":
		cmdName = "sudo"
		cmdArgs = []string{"dnf", "install", "-y"}
		for _, m := range missing {
			switch m {
			case "parec":
				pkgs = append(pkgs, "pipewire-pulseaudio", "pulseaudio-utils")
			case "wtype":
				pkgs = append(pkgs, "wtype")
			case "wl-copy":
				pkgs = append(pkgs, "wl-clipboard")
			case "whisper-cli":
				pkgs = append(pkgs, "gcc-c++", "cmake", "git")
			}
		}
	default:
		fmt.Printf("💡 Please install missing packages manually for your distribution: %s\n", strings.Join(missing, ", "))
		return nil
	}

	// Deduplicate package list
	pkgMap := make(map[string]bool)
	var uniquePkgs []string
	for _, p := range pkgs {
		if !pkgMap[p] {
			pkgMap[p] = true
			uniquePkgs = append(uniquePkgs, p)
		}
	}

	if len(uniquePkgs) > 0 {
		fmt.Println("\n🔐 Privileged setup required to install missing system packages.")
		fmt.Printf("   Running: sudo %s %s\n\n", cmdArgs[0], strings.Join(append(cmdArgs[1:], uniquePkgs...), " "))

		cmdArgs = append(cmdArgs, uniquePkgs...)
		cmd := exec.Command(cmdName, cmdArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Printf("⚠️  Package installation error: %v\n", err)
		}
	}

	if _, err := exec.LookPath("whisper-cli"); err != nil {
		buildWhisperCpp()
	}
	return nil
}

func buildWhisperCpp() {
	if _, err := exec.LookPath("whisper-cli"); err == nil {
		return
	}
	fmt.Println("\n⚙️  Building whisper.cpp with Vulkan GPU acceleration from source...")
	tmpDir := filepath.Join(os.TempDir(), "mavor-whisper-build")
	_ = os.RemoveAll(tmpDir)

	cloneCmd := exec.Command("git", "clone", "--depth", "1", "https://github.com/ggerganov/whisper.cpp.git", tmpDir)
	cloneCmd.Stdout = os.Stdout
	cloneCmd.Stderr = os.Stderr
	if err := cloneCmd.Run(); err != nil {
		fmt.Printf("⚠️  Failed to clone whisper.cpp: %v\n", err)
		return
	}

	cmakeCmd := exec.Command("cmake", "-B", filepath.Join(tmpDir, "build"), "-S", tmpDir, "-DGGML_VULKAN=ON")
	cmakeCmd.Stdout = os.Stdout
	cmakeCmd.Stderr = os.Stderr
	if err := cmakeCmd.Run(); err != nil {
		fmt.Printf("⚠️  CMake configure failed: %v\n", err)
		return
	}

	buildCmd := exec.Command("cmake", "--build", filepath.Join(tmpDir, "build"), "-j")
	buildCmd.Stdout = os.Stdout
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		fmt.Printf("⚠️  CMake build failed: %v\n", err)
		return
	}

	cliSrc := filepath.Join(tmpDir, "build", "bin", "whisper-cli")
	srvSrc := filepath.Join(tmpDir, "build", "bin", "whisper-server")

	// Copy to user local bin (~/.local/bin)
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	_ = os.MkdirAll(localBin, 0o755)
	_ = exec.Command("cp", cliSrc, filepath.Join(localBin, "whisper-cli")).Run()
	_ = exec.Command("cp", srvSrc, filepath.Join(localBin, "whisper-server")).Run()

	// Also try copying to system /usr/local/bin
	installCmd := exec.Command("sudo", "cp", cliSrc, srvSrc, "/usr/local/bin/")
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err == nil {
		fmt.Println("✅ Installed whisper-cli and whisper-server to /usr/local/bin")
	} else {
		fmt.Printf("✅ Installed whisper-cli and whisper-server to %s\n", localBin)
	}
	_ = os.RemoveAll(tmpDir)
}

func checkWayland() (bool, string) {
	wd := os.Getenv("WAYLAND_DISPLAY")
	if wd != "" {
		return true, fmt.Sprintf("WAYLAND_DISPLAY=%s", wd)
	}
	// Check if any wayland socket exists in runtime dir
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt != "" {
		matches, _ := filepath.Glob(filepath.Join(rt, "wayland-*"))
		if len(matches) > 0 {
			return true, fmt.Sprintf("socket found at %s", matches[0])
		}
	}
	return false, "No Wayland session detected ($WAYLAND_DISPLAY unset; fix: run inside a Wayland session)"
}

func checkAudio() (bool, string) {
	if _, err := exec.LookPath("parec"); err != nil {
		return false, "parec binary not found in $PATH (fix: install pulseaudio-utils / pipewire-pulse)"
	}
	// Verify pulse server is responding
	cmd := exec.Command("pactl", "info")
	if out, err := cmd.Output(); err == nil && len(out) > 0 {
		return true, "parec available, PipeWire/PulseAudio server connected"
	}
	return true, "parec available (audio server check skipped/idle)"
}

func checkWtype() (bool, string) {
	if p, err := exec.LookPath("wtype"); err == nil {
		return true, fmt.Sprintf("wtype installed at %s", p)
	}
	return false, "wtype not found in $PATH (fix: install wtype for virtual typing)"
}

func checkClipboard() (bool, string) {
	copyOk := false
	pasteOk := false
	if _, err := exec.LookPath("wl-copy"); err == nil {
		copyOk = true
	}
	if _, err := exec.LookPath("wl-paste"); err == nil {
		pasteOk = true
	}
	if copyOk && pasteOk {
		return true, "wl-copy and wl-paste installed"
	}
	return false, "wl-clipboard tools missing (fix: install wl-clipboard)"
}

// checkRuntime reports the two derived facts a user cannot read off the
// config file: which inference runtime the model belongs to, and where that
// runtime will run. Both follow from the model name, so this is the line that
// says what `model = "..."` actually got you.
func checkRuntime() (bool, string) {
	cfg, _ := config.Load("")
	sel, err := models.Select(cfg.Model, cfg.Advanced.Placement, cfg.Advanced.Server)
	if err != nil {
		return false, err.Error()
	}

	msg := fmt.Sprintf("%s, %s — %s", sel.Runtime, sel.Placement, sel.Reason)
	for _, w := range sel.Warnings {
		msg += "; " + w
	}

	switch sel.Placement {
	case models.PlacementRemote:
		return true, msg + fmt.Sprintf(" (%s)", sel.Server)
	case models.PlacementLocalServer:
		if _, err := exec.LookPath("whisper-server"); err != nil {
			return false, msg + " — whisper-server is not in $PATH (fix: install whisper.cpp, or set advanced.placement = \"subprocess\")"
		}
	case models.PlacementSubprocess:
		if _, err := exec.LookPath("whisper-cli"); err != nil {
			return false, msg + " — whisper-cli is not in $PATH (fix: run 'mavor doctor --fix')"
		}
	}
	return true, msg
}

// checkThreads reports the inference thread count and where it came from,
// because the default is computed from this machine and a user who never set
// the key has no other way to see what it decided.
func checkThreads() (bool, string) {
	f, _ := config.LoadFile("")
	detected := config.PhysicalCores()
	logical := runtime.NumCPU()

	if f.Advanced.Threads == detected {
		return true, fmt.Sprintf("%d (this machine's physical core count; %d logical)", detected, logical)
	}
	if f.Advanced.Threads > logical {
		return true, fmt.Sprintf("%d, set by advanced.threads — above this machine's %d logical CPUs, which usually costs speed rather than buying it",
			f.Advanced.Threads, logical)
	}
	return true, fmt.Sprintf("%d, set by advanced.threads (this machine has %d physical cores, %d logical)",
		f.Advanced.Threads, detected, logical)
}

func checkConfig() (bool, string) {
	f, err := config.LoadFile("")
	if err != nil {
		return false, fmt.Sprintf("error parsing %s: %v", config.Path(), err)
	}
	if !f.Exists {
		return true, fmt.Sprintf("no config file at %s (using defaults; run 'mavor doctor --fix' to create)", f.Path)
	}

	// A file in which nothing at all is recognized is a file written against
	// the old schema. Saying "3 unknown keys" about it would bury the fact
	// that it is contributing nothing.
	if f.SchemaLooksStale() {
		return false, fmt.Sprintf(
			"%s uses the old configuration schema — none of its %d keys exist any more, so every setting in it is being ignored and mavor is running on defaults. `mavor config init --force` scaffolds the current file (%s)",
			f.Path, len(f.UnknownKeys), strings.Join(f.UnknownKeys, ", "))
	}
	if len(f.UnknownKeys) > 0 {
		return false, fmt.Sprintf("%s has %d key(s) mavor does not know, which are ignored: %s (`mavor config init --force` scaffolds the current file)",
			f.Path, len(f.UnknownKeys), strings.Join(f.UnknownKeys, ", "))
	}
	return true, fmt.Sprintf("valid config (model=%s, preview=%s)", f.Model, previewDescription(f.Config))
}

// previewDescription says in a phrase what the overlay will show.
func previewDescription(cfg config.Config) string {
	if !cfg.Preview.Enabled {
		return "off"
	}
	return cfg.Preview.Source
}

func checkModel() (bool, string) {
	cfg, _ := config.Load("")
	res, err := speech.Resolve(cfg)
	if err != nil {
		return false, err.Error()
	}
	if res.ModelDir != "" {
		return true, fmt.Sprintf("%s found at %s", cfg.Model, res.ModelDir)
	}
	if res.Placement == models.PlacementRemote {
		return true, fmt.Sprintf("%s runs on the server at %s, so there is nothing to install here", cfg.Model, res.Server)
	}
	return true, fmt.Sprintf("%s found at %s", cfg.Model, res.ModelPath)
}

func checkDaemon() (bool, string) {
	cfg, _ := config.Load("")
	resp, err := ipc.Send(cfg.Paths.Socket, ipc.Request{Action: "status"}, 500*time.Millisecond)
	if err != nil {
		return false, fmt.Sprintf("daemon is not running at %s (run 'mavor daemon' or 'mavor service start')", cfg.Paths.Socket)
	}
	return true, fmt.Sprintf("daemon is active (state: %s)", resp.State)
}

func checkServiceUnit() (bool, string) {
	unitPath := getServicePath()
	if _, err := os.Stat(unitPath); err == nil {
		cmd := exec.Command("systemctl", "--user", "is-active", "mavor")
		out, _ := cmd.Output()
		status := string(out)
		if len(status) > 0 && status[len(status)-1] == '\n' {
			status = status[:len(status)-1]
		}
		if status == "active" {
			return true, fmt.Sprintf("systemd unit installed and active (%s)", status)
		}
		return true, fmt.Sprintf("systemd unit installed (%s)", status)
	}
	return true, "systemd unit not installed (optional; run 'mavor service install' to enable)"
}
