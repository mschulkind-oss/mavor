package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/daemon"
)

// fakeICD writes a manifest at <prefix>/share/vulkan/icd.d/<name> whose
// library_path is libPath, and returns the manifest path. It creates the
// library too unless libPath is absolute and withLib is false — the "the
// manifest survived but the driver was garbage-collected" case.
func fakeICD(t *testing.T, prefix, name, libPath string, withLib bool) string {
	t.Helper()
	dir := filepath.Join(prefix, "share", "vulkan", "icd.d")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(dir, name)
	body := `{"file_format_version":"1.0.1","ICD":{"api_version":"1.4.354","library_path":"` + libPath + `"}}`
	if err := os.WriteFile(manifest, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if withLib {
		lib := libPath
		if !filepath.IsAbs(lib) {
			lib = filepath.Join(dir, lib)
		}
		if err := os.MkdirAll(filepath.Dir(lib), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(lib, []byte("not really an elf"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return manifest
}

func TestICDSearchPrefersAnExactManifest(t *testing.T) {
	root := t.TempDir()
	want := fakeICD(t, root, radeonICD, filepath.Join(root, "lib", "libvulkan_radeon.so"), true)

	s := icdSearch{
		driverName: radeonICD,
		manifests:  []string{filepath.Join(root, "nope", "share", "vulkan", "icd.d", radeonICD), want},
	}
	if got := s.find(); got != want {
		t.Errorf("find() = %q, want %q", got, want)
	}
}

// The signal this pins is the one that makes the nix layout work at all: a
// merged /lib whose entry is a symlink into the store path of the mesa that is
// actually installed. Resolving it is what tells the search which of several
// store mesas to trust.
func TestICDSearchResolvesTheDriverLibSymlink(t *testing.T) {
	root := t.TempDir()
	store := filepath.Join(root, "store", "aaaa-mesa-26.2.2")
	lib := filepath.Join(store, "lib", "libvulkan_radeon.so")
	want := fakeICD(t, store, radeonICD, lib, true)

	merged := filepath.Join(root, "lib")
	if err := os.MkdirAll(merged, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(merged, "libvulkan_radeon.so")
	if err := os.Symlink(lib, link); err != nil {
		t.Fatal(err)
	}

	s := icdSearch{driverName: radeonICD, driverLibs: []string{link}}
	got, err := filepath.EvalSymlinks(s.find())
	if err != nil {
		t.Fatalf("find() returned %q: %v", s.find(), err)
	}
	if wantResolved, _ := filepath.EvalSymlinks(want); got != wantResolved {
		t.Errorf("find() = %q, want %q", got, wantResolved)
	}
}

// A store scan is the last resort, and the store holds every mesa any previous
// image ever baked. The newest is preferred, and one whose driver library has
// been garbage-collected is passed over rather than handed to the loader —
// that manifest fails exactly like no manifest, silently, as "no GPU".
func TestICDSearchSkipsAStoreEntryWhoseDriverIsGone(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "store", "aaaa-mesa-26.2.0")
	stale := filepath.Join(root, "store", "zzzz-mesa-26.2.2")
	want := fakeICD(t, good, radeonICD, filepath.Join(good, "lib", "libvulkan_radeon.so"), true)
	fakeICD(t, stale, radeonICD, filepath.Join(stale, "lib", "libvulkan_radeon.so"), false)

	s := icdSearch{driverName: radeonICD, prefixGlobs: []string{filepath.Join(root, "store", "*-mesa-*")}}
	if got := s.find(); got != want {
		t.Errorf("find() = %q, want the entry whose driver still exists, %q", got, want)
	}
}

// A manifest may name the driver by bare filename and leave the search to the
// loader. That is legal and we cannot check it, so it must not be rejected.
func TestICDSearchAcceptsABareLibraryName(t *testing.T) {
	root := t.TempDir()
	want := fakeICD(t, root, radeonICD, "libvulkan_radeon.so", false)

	s := icdSearch{driverName: radeonICD, manifests: []string{want}}
	if got := s.find(); got != want {
		t.Errorf("find() = %q, want %q", got, want)
	}
}

func TestICDSearchFindsNothingWhenThereIsNothing(t *testing.T) {
	root := t.TempDir()
	s := icdSearch{
		driverName:  radeonICD,
		manifests:   []string{filepath.Join(root, "share", "vulkan", "icd.d", radeonICD)},
		driverLibs:  []string{filepath.Join(root, "lib", "libvulkan_radeon.so")},
		prefixGlobs: []string{filepath.Join(root, "store", "*-mesa-*")},
	}
	if got := s.find(); got != "" {
		t.Errorf("find() = %q, want \"\" on an empty tree", got)
	}
}

// lvp_icd is llvmpipe, a software rasterizer that enumerates as an ordinary
// VkPhysicalDevice. It sits in the same directory as the real driver, so a
// search that widened to "any manifest in icd.d" would publish CPU numbers
// under a GPU heading — the failure AGENTS.md's proof-of-a-GPU rule names.
func TestICDSearchIgnoresTheSoftwareRasterizer(t *testing.T) {
	root := t.TempDir()
	fakeICD(t, root, "lvp_icd.x86_64.json", filepath.Join(root, "lib", "libvulkan_lvp.so"), true)

	s := icdSearch{
		driverName:  radeonICD,
		manifests:   []string{filepath.Join(root, "share", "vulkan", "icd.d", radeonICD)},
		prefixGlobs: []string{root},
	}
	if got := s.find(); got != "" {
		t.Errorf("find() = %q, want \"\" — llvmpipe is not a GPU", got)
	}
}

func TestVulkanEnvDefersToTheCaller(t *testing.T) {
	t.Setenv("VK_ICD_FILENAMES", "/somewhere/of/my/own.json")
	if got := vulkanEnv(); got != nil {
		t.Errorf("vulkanEnv() = %v, want nil when the caller already set VK_ICD_FILENAMES", got)
	}
	t.Setenv("VK_ICD_FILENAMES", "")
	t.Setenv("VK_DRIVER_FILES", "/somewhere/else.json")
	if got := vulkanEnv(); got != nil {
		t.Errorf("vulkanEnv() = %v, want nil when the caller already set VK_DRIVER_FILES", got)
	}
}

// The report's "what this run could not measure" table has to tell a reader
// which of these it was, because a loader with no driver and a machine with no
// GPU look identical in the output and take opposite actions to fix.
func TestAbsenceReasonDistinguishesTheCauses(t *testing.T) {
	for _, tc := range []struct {
		name           string
		haveVulkaninfo bool
		icdEnv         string
		icdFound       string
		want           string
	}{
		{"no tool", false, "", "", "vulkan-tools"},
		{"caller's own setting", true, "/mine.json", "", "/mine.json"},
		{"driver found, no device", true, "", "/share/x.json", "/share/x.json"},
		{"loader has no driver", true, "", "", "Found no drivers!"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := absenceReason(tc.haveVulkaninfo, tc.icdEnv, tc.icdFound)
			if !strings.Contains(got, tc.want) {
				t.Errorf("reason %q does not mention %q", got, tc.want)
			}
		})
	}
}

// The one branch that used to read as "there is no GPU here" when the truth
// was "the loader had nothing to load" has to name a thing the reader can do.
func TestAbsenceReasonForNoDriverIsActionable(t *testing.T) {
	got := absenceReason(true, "", "")
	for _, want := range []string{"VK_ICD_FILENAMES", "icd.d", radeonICD, "passed through"} {
		if !strings.Contains(got, want) {
			t.Errorf("reason %q omits %q, so a reader cannot act on it", got, want)
		}
	}
}

// vulkanAbsenceReason is the wiring around absenceReason; all it has to do is
// pass the caller's setting through, whatever this machine's hardware is.
func TestVulkanAbsenceReasonReadsTheEnvironment(t *testing.T) {
	if _, err := exec.LookPath("vulkaninfo"); err != nil {
		t.Skip("no vulkaninfo on PATH; that absence outranks every other reason")
	}
	t.Setenv("VK_ICD_FILENAMES", "/somewhere/of/my/own.json")
	if got := vulkanAbsenceReason(); !strings.Contains(got, "/somewhere/of/my/own.json") {
		t.Errorf("reason %q does not name the VK_ICD_FILENAMES that was in force", got)
	}
}

// The streaming rows exist to answer "does this model feel live", and the
// report tells the reader they were fed the way the daemon feeds. That was
// false for as long as the harness used 100 ms chunks against the daemon's
// 30 ms tick, and nothing caught it because the two numbers lived in
// different packages with no link between them. This is the link.
func TestStreamingChunkMatchesTheDaemonsPreviewTick(t *testing.T) {
	want := int(daemon.PreviewTick / time.Millisecond)
	if streamChunkMS != want {
		t.Errorf("streamChunkMS = %d ms, but the daemon feeds the preview every %d ms — "+
			"the streaming numbers would describe a cadence no user experiences, "+
			"and the report would claim otherwise", streamChunkMS, want)
	}
}
