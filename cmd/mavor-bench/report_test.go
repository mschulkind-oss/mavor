package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleReport() *report {
	return &report{
		Machine: machineInfo{
			Timestamp: time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC),
			Hostname:  "testhost",
			OS:        "linux",
			Arch:      "amd64",
			CPUModel:  "Test CPU",
			CPUCores:  12,
			GPUName:   "Test GPU",
			GPUDriver: "radv",
		},
		Audio:              "test/fixtures/real_speech.wav",
		AudioSeconds:       20,
		RunsPerCell:        3,
		Threads:            6,
		StreamChunkMS:      100,
		WhisperGPUBackends: []string{"Vulkan"},
		Results: []runResult{
			{Model: "base.en", Backend: backend{Engine: "whisper-cli", Device: "cpu", Build: "stock", Mode: "batch"},
				Runs: 3, TotalMS: 1500, RTF: 0.075, PeakRSSKB: 300 * 1024, WER: 0.02, CapF1: 1},
			{Model: "base.en", Backend: backend{Engine: "whisper-cli", Device: "gpu", Build: "vulkan", Mode: "batch"},
				Runs: 3, TotalMS: 500, RTF: 0.025, PeakRSSKB: 130 * 1024, WER: 0.02, CapF1: 1},
			{Model: "zipformer", Backend: backend{Engine: "sherpa", Device: "cpu", Mode: "batch"},
				Runs: 3, TotalMS: 400, RTF: 0.02, PeakRSSKB: 200 * 1024, WER: 0.1},
			{Model: "zipformer", Backend: backend{Engine: "sherpa", Device: "cpu", Mode: "streaming"},
				Runs: 3, TotalMS: 450, FirstTokenMS: 120, RTF: 0.022, PeakRSSKB: 200 * 1024, WER: 0.1},
			{Model: "canary-1b", Backend: backend{Engine: "sherpa", Device: "cpu", Mode: "batch"},
				Failed: true, Error: "model type detection failed"},
		},
		NotInstalled: []string{"large-v3"},
		Skipped:      []skipNote{{"sherpa / gpu", "CPU-only ONNX Runtime"}},
	}
}

func renderReport(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "report.md")
	if err := writeMarkdown(path, sampleReport()); err != nil {
		t.Fatalf("writeMarkdown: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestReportStatesWhatItCouldNotMeasure(t *testing.T) {
	out := renderReport(t)
	// The whole point of the rewrite: absent things are named, not omitted.
	if !strings.Contains(out, "could not measure") {
		t.Error("report has no section for what it could not measure")
	}
	if !strings.Contains(out, "CPU-only ONNX Runtime") {
		t.Error("report omits the reason a backend was skipped")
	}
	if !strings.Contains(out, "large-v3") {
		t.Error("report omits the models that were not downloaded")
	}
}

func TestReportSeparatesFailedCellsFromMissingOnes(t *testing.T) {
	out := renderReport(t)
	if !strings.Contains(out, "Cells that failed") {
		t.Fatal("report has no failures section")
	}
	if !strings.Contains(out, "model type detection failed") {
		t.Error("report drops the error from a failed cell instead of showing it")
	}
	// A failed cell must not appear in the speed table as if it had a time.
	speed := section(out, "## Speed", "## Memory")
	if strings.Contains(speed, "canary-1b") {
		t.Error("a failed model appears in the speed table; it has no timing to report")
	}
}

func TestReportCarriesTheMachineFingerprint(t *testing.T) {
	out := renderReport(t)
	// Without this a rerun elsewhere cannot be compared to this one.
	for _, want := range []string{"Test CPU", "Test GPU", "testhost", "12 logical cores"} {
		if !strings.Contains(out, want) {
			t.Errorf("report omits %q from the machine block", want)
		}
	}
}

func TestReportGivesStreamingItsOwnSectionWithFirstToken(t *testing.T) {
	out := renderReport(t)
	streaming := section(out, "## Streaming vs batch", "## Cells that failed")
	if streaming == "" {
		t.Fatal("report has no streaming section")
	}
	if !strings.Contains(streaming, "First token") {
		t.Error("streaming section omits time to first token, which is the number it exists for")
	}
	if !strings.Contains(streaming, "120 ms") {
		t.Error("streaming section does not show the measured first-token time")
	}
	// Batch and streaming for the same model are compared side by side.
	if !strings.Contains(streaming, "400 ms") {
		t.Error("streaming section does not show the batch total it is being compared against")
	}
}

func TestReportSaysGPURowsAreNotVRAM(t *testing.T) {
	// A memory table next to a GPU column invites exactly one wrong reading.
	out := renderReport(t)
	memory := section(out, "## Memory", "## Accuracy")
	if !strings.Contains(memory, "VRAM") {
		t.Error("memory section does not say that the GPU rows are host memory, not VRAM")
	}
}

func TestReportMarksItselfGeneratedSoNobodyHandEditsIt(t *testing.T) {
	out := renderReport(t)
	if !strings.HasPrefix(out, "---\n") {
		t.Error("report has no YAML frontmatter")
	}
	if !strings.Contains(out, "status: generated") {
		t.Error("report frontmatter does not mark it as generated")
	}
	if !strings.Contains(out, "Do not edit it by hand") {
		t.Error("report does not warn against hand-editing")
	}
}

func TestWriteJSONRoundTripsEveryResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.json")
	if err := writeJSON(path, sampleReport()); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// The JSON is the artifact a future run is diffed against, so the failed
	// cell has to survive it too.
	for _, want := range []string{"base.en", "zipformer", "canary-1b", "first_token_ms", "peak_rss_kb"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON output omits %q", want)
		}
	}
}

func TestMemStringUsesTheRightUnits(t *testing.T) {
	// Kilobytes in, human units out — the conversion that is easy to get
	// wrong by a factor of 1024.
	if got := memString(300 * 1024); got != "300 MB" {
		t.Errorf("memString(300 MB in KB) = %q, want \"300 MB\"", got)
	}
	if got := memString(2 * 1024 * 1024); got != "2.00 GB" {
		t.Errorf("memString(2 GB in KB) = %q, want \"2.00 GB\"", got)
	}
	if got := memString(0); got != "—" {
		t.Errorf("memString(0) = %q, want a dash for unmeasured", got)
	}
}

// section returns the text between two headings, so a test can assert that a
// row is in one table and not another.
func section(doc, from, to string) string {
	i := strings.Index(doc, from)
	if i < 0 {
		return ""
	}
	rest := doc[i:]
	if j := strings.Index(rest, to); j > 0 {
		return rest[:j]
	}
	return rest
}
