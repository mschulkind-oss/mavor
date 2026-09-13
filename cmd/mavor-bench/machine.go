package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if path := defaultICDSearch().find(); path != "" {
		return []string{"VK_ICD_FILENAMES=" + path}
	}
	return nil
}

// radeonICD is the manifest basename mesa installs for RADV, the AMD Vulkan
// driver. It is the only name searched for, and that is deliberate rather than
// unfinished: mesa ships a dozen manifests side by side and one of them,
// lvp_icd, is llvmpipe — a software rasterizer that enumerates as a perfectly
// ordinary VkPhysicalDevice. Pointing the loader at the whole icd.d directory
// makes llvmpipe a "GPU", and the resulting column would be CPU numbers under
// a GPU heading, which is the exact failure the "proof of a GPU" rule in
// AGENTS.md exists to prevent. Supporting another real GPU vendor means adding
// its manifest name here on a machine where it can be checked — never a
// wildcard over the directory.
const radeonICD = "radeon_icd.x86_64.json"

// icdSearch is where vulkanEnv looks for that manifest. It is a value with
// injectable roots rather than a literal list inside the function so a test
// can aim the whole search at a temp directory shaped like a real one; nothing
// about the search needs a GPU to exercise.
type icdSearch struct {
	// driverName is the manifest basename to look for under a
	// share/vulkan/icd.d directory.
	driverName string

	// manifests are exact paths, tried first and in order.
	manifests []string

	// driverLibs are paths to the driver's shared object. This is the
	// interesting one on a nix system. /lib and /usr/lib in this image are
	// symlink farms the image builder fills in from the packages it baked, so
	// /lib/libvulkan_radeon.so resolves into the store path of the mesa that
	// is actually installed — a signal, not a guess. The store itself holds
	// every mesa any previous image ever used, so the resolved
	// symlink is strictly better than globbing for one: the glob would have to
	// pick, and picking wrong means an ICD whose library_path names a
	// garbage-collected file.
	driverLibs []string

	// prefixGlobs are package-prefix patterns, scanned only when neither of
	// the above matched — a plain NixOS host with no merged /lib. Matches are
	// tried newest-looking first (Glob sorts, so the highest version string
	// comes last) and each is validated before it is used.
	prefixGlobs []string
}

func defaultICDSearch() icdSearch {
	return icdSearch{
		driverName: radeonICD,
		manifests: []string{
			// The jail image's merged share tree: a symlink farm the image
			// rebuilds from whatever mesa it baked, so it carries no store
			// hash and survives a rebuild.
			"/share/vulkan/icd.d/" + radeonICD,
			// NixOS proper.
			"/run/opengl-driver/share/vulkan/icd.d/" + radeonICD,
			// Everywhere else. The loader searches this one itself; it is
			// listed so a run on a normal distro takes the first branch and
			// never reaches the store scan.
			"/usr/share/vulkan/icd.d/" + radeonICD,
		},
		driverLibs: []string{
			"/lib/libvulkan_radeon.so",
			"/usr/lib/libvulkan_radeon.so",
			"/run/opengl-driver/lib/libvulkan_radeon.so",
		},
		prefixGlobs: []string{"/nix/store/*-mesa-*"},
	}
}

// find returns the first usable ICD manifest, or "" when there is none. It
// only helps the loader locate a driver: whether a device then enumerates is
// still decided by vulkaninfo, and whether a GPU column may be published is
// still decided by hasGPUBackend and a non-empty GPUName.
func (s icdSearch) find() string {
	for _, path := range s.manifests {
		if usableICD(path) {
			return path
		}
	}
	for _, lib := range s.driverLibs {
		resolved, err := filepath.EvalSymlinks(lib)
		if err != nil {
			continue
		}
		// <prefix>/lib/libvulkan_radeon.so -> <prefix>
		path := s.manifestUnder(filepath.Dir(filepath.Dir(resolved)))
		if usableICD(path) {
			return path
		}
	}
	for _, pattern := range s.prefixGlobs {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for i := len(matches) - 1; i >= 0; i-- {
			path := s.manifestUnder(matches[i])
			if usableICD(path) {
				return path
			}
		}
	}
	return ""
}

func (s icdSearch) manifestUnder(prefix string) string {
	return filepath.Join(prefix, "share", "vulkan", "icd.d", s.driverName)
}

// usableICD reports whether a manifest exists AND names a driver library that
// is still on disk. The second half is what a bare os.Stat missed: a store
// path left behind by an old image has a perfectly readable manifest pointing
// at a library that was garbage-collected, and handing that to the loader
// fails exactly like finding no manifest at all — silently, as no GPU.
func usableICD(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var manifest struct {
		ICD struct {
			LibraryPath string `json:"library_path"`
		} `json:"ICD"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return false
	}
	lib := manifest.ICD.LibraryPath
	if lib == "" {
		return false
	}
	// A bare filename means "let the loader search its own library path",
	// which is a legal manifest we cannot check and must not reject.
	if !strings.ContainsRune(lib, filepath.Separator) {
		return true
	}
	if !filepath.IsAbs(lib) {
		lib = filepath.Join(filepath.Dir(path), lib)
	}
	_, err = os.Stat(lib)
	return err == nil
}

// vulkanAbsenceReason says why no Vulkan device was found, in terms a reader
// of the report can act on. "No GPU column" has three quite different causes
// and they need different responses: a missing tool, a loader with no driver
// to load, and a machine that genuinely has no device. Reporting the middle
// one as the last is how a passed-through GPU goes unbenchmarked for weeks.
func vulkanAbsenceReason() string {
	_, err := exec.LookPath("vulkaninfo")
	icdEnv := os.Getenv("VK_ICD_FILENAMES")
	if icdEnv == "" {
		icdEnv = os.Getenv("VK_DRIVER_FILES")
	}
	return absenceReason(err == nil, icdEnv, defaultICDSearch().find())
}

// absenceReason is vulkanAbsenceReason with the three probes passed in, so
// every branch is reachable from a test on a machine with any hardware at all.
func absenceReason(haveVulkaninfo bool, icdEnv, icdFound string) string {
	switch {
	case !haveVulkaninfo:
		return "`vulkaninfo` is not on PATH, so no device could be proven; install vulkan-tools and rerun"
	case icdEnv != "":
		return fmt.Sprintf("`%s` came from the environment and enumerated no device", icdEnv)
	case icdFound != "":
		return fmt.Sprintf("the loader was pointed at `%s` and still enumerated no device", icdFound)
	default:
		return fmt.Sprintf("no Vulkan ICD manifest was found — `VK_ICD_FILENAMES` is unset and there is no `%s` "+
			"on any searched path, so the loader had no driver to load and would fail with "+
			"\"Found no drivers!\" even on a machine whose GPU is passed through. Point "+
			"`VK_ICD_FILENAMES` at your driver’s manifest (mesa installs it under "+
			"`<prefix>/share/vulkan/icd.d/`) and rerun", radeonICD)
	}
}
