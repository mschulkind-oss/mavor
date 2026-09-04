package main

import (
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// machineInfo is the fingerprint of where a run happened. It exists because
// the point of this harness is comparing runs — this machine against itself
// after a change, or against someone else's hardware — and a table of
// milliseconds means nothing without it. Every field is probed, never
// assumed; a field that could not be determined stays empty rather than being
// filled with a plausible default.
type machineInfo struct {
	Timestamp   time.Time `json:"timestamp"`
	Hostname    string    `json:"hostname"`
	OS          string    `json:"os"`
	Arch        string    `json:"arch"`
	CPUModel    string    `json:"cpu_model"`
	CPUCores    int       `json:"cpu_logical_cores"`
	MemTotalKB  int64     `json:"mem_total_kb"`
	GPUName     string    `json:"gpu_name,omitempty"`
	GPUDriver   string    `json:"gpu_driver,omitempty"`
	VulkanAPI   string    `json:"vulkan_api_version,omitempty"`
	MavorCommit string    `json:"mavor_commit,omitempty"`

	// GoVersion matters for the sherpa path, which is in-process CGO: the
	// number is as much a property of this toolchain as of the model.
	GoVersion string `json:"go_version"`
}

func collectMachineInfo() machineInfo {
	m := machineInfo{
		Timestamp: time.Now().UTC(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
		CPUCores:  runtime.NumCPU(),
		GoVersion: runtime.Version(),
	}
	m.Hostname, _ = os.Hostname()
	m.CPUModel = cpuModel()
	m.MemTotalKB = memTotalKB()
	m.MavorCommit = gitCommit()
	m.GPUName, m.GPUDriver, m.VulkanAPI = vulkanDevice()
	return m
}

// cpuModel reads /proc/cpuinfo. On a non-Linux host this returns empty, which
// the report renders as "unknown" rather than inventing a name.
func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, val, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(name) == "model name" {
			return strings.TrimSpace(val)
		}
	}
	return ""
}

func memTotalKB() int64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				n, _ := strconv.ParseInt(fields[1], 10, 64)
				return n
			}
		}
	}
	return 0
}

func gitCommit() string {
	out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

var (
	reDeviceName = regexp.MustCompile(`deviceName\s*=\s*(.+)`)
	reDriverName = regexp.MustCompile(`driverName\s*=\s*(.+)`)
	reAPIVersion = regexp.MustCompile(`apiVersion\s*=\s*(\S+)`)
)

// vulkanDevice asks vulkaninfo what GPU is actually present. This is the
// check that separates "the GPU column is real" from "the binary was built
// with a GPU backend and silently fell back to CPU" — the exact confusion
// that produced the fabricated reports this harness replaces. No device here
// means the report says so and runs CPU only.
func vulkanDevice() (name, driver, apiVersion string) {
	bin, err := exec.LookPath("vulkaninfo")
	if err != nil {
		return "", "", ""
	}
	cmd := exec.Command(bin, "--summary")
	cmd.Env = append(os.Environ(), vulkanEnv()...)
	out, err := cmd.Output()
	if err != nil {
		return "", "", ""
	}
	text := string(out)
	if m := reDeviceName.FindStringSubmatch(text); m != nil {
		name = strings.TrimSpace(m[1])
	}
	if m := reDriverName.FindStringSubmatch(text); m != nil {
		driver = strings.TrimSpace(m[1])
	}
	if m := reAPIVersion.FindStringSubmatch(text); m != nil {
		apiVersion = strings.TrimSpace(m[1])
	}
	return name, driver, apiVersion
}

// vulkanEnv points the Vulkan loader at an ICD when the environment has not
// already done so. A NixOS container has no /usr/share/vulkan/icd.d, so the
// loader finds no driver and reports no GPU even though the device node is
// right there — which looks identical to having no GPU at all. VK_ICD_FILENAMES
// from the caller always wins; this only fills in a blank.
func vulkanEnv() []string {
	if os.Getenv("VK_ICD_FILENAMES") != "" || os.Getenv("VK_DRIVER_FILES") != "" {
		return nil
	}
	for _, candidate := range []string{
		"/share/vulkan/icd.d/radeon_icd.x86_64.json",
		"/run/opengl-driver/share/vulkan/icd.d/radeon_icd.x86_64.json",
		"/usr/share/vulkan/icd.d/radeon_icd.x86_64.json",
	} {
		if _, err := os.Stat(candidate); err == nil {
			return []string{"VK_ICD_FILENAMES=" + candidate}
		}
	}
	return nil
}
