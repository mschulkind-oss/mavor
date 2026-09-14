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
				Runs: 3, TotalMS: 450, FirstTokenMS: 120, RTF: 0.022, PeakRSSKB: 200 * 1024, WER: 0.1,
				Updates: 53, MeanGapMS: 357, MaxGapMS: 660, SlowestChunkMS: 23},
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
	if !strings.Contains(memory, "getrusage") {
		t.Error("memory section does not say how peak RSS was measured")
	}
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

func TestReportSaysStreamingWasUnmeasuredRatherThanOmittingIt(t *testing.T) {
	// The failure this guards against: every streaming model fails to load,
	// the section renders nothing, and the reader concludes streaming was
	// never part of the run instead of that it could not be measured.
	r := sampleReport()
	var kept []runResult
	for _, x := range r.Results {
		if x.Backend.Mode == "streaming" {
			x.Failed, x.Error = true, "model could not be loaded"
		}
		kept = append(kept, x)
	}
	r.Results = kept

	path := filepath.Join(t.TempDir(), "r.md")
	if err := writeMarkdown(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := string(data)

	if !strings.Contains(out, "## Streaming vs batch") {
		t.Fatal("the streaming section disappeared when every streaming model failed")
	}
	if !strings.Contains(out, "Not measured") {
		t.Error("the streaming section does not say the measurement did not happen")
	}
	if !strings.Contains(out, "This is a finding") {
		t.Error("the streaming section does not flag unloadable streaming models as a finding")
	}
}

func TestReportSaysSoWhenNothingStreamsAtAll(t *testing.T) {
	// Distinct from the case above: a run with no streaming models is not a
	// failure, and must not be reported as one.
	r := sampleReport()
	var kept []runResult
	for _, x := range r.Results {
		if x.Backend.Mode != "streaming" {
			kept = append(kept, x)
		}
	}
	r.Results = kept

	path := filepath.Join(t.TempDir(), "r.md")
	if err := writeMarkdown(path, r); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	out := string(data)

	if !strings.Contains(out, "No model in this run is marked as streaming") {
		t.Error("a run with no streaming models does not explain why the section is empty")
	}
	if strings.Contains(out, "This is a finding") {
		t.Error("a run with no streaming models is reported as a failure; it is not one")
	}
}

// renderToString is the shape the tests around it already use: write the
// report to a temp file and read it back, so the assertion runs against what
// a reader would actually get.
func renderToString(t *testing.T, r *report) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "r.md")
	if err := writeMarkdown(path, r); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A sweep measured one model 2.5x slower than a direct measurement taken
// minutes later on the same binary, because the host sat at a load average of
// 14 while it ran. Nothing in the report said so, and the median of three
// runs smooths jitter but not sustained contention — so the row read as a
// slow model rather than a busy machine.
func TestReportWarnsWhenTheMachineWasBusy(t *testing.T) {
	r := sampleReport()
	r.Machine.CPUCores = 12
	r.Threads = 6
	r.Machine.LoadBefore = 14.5
	r.Machine.LoadAfter = 11.0

	out := renderToString(t, r)
	if !strings.Contains(out, "14.50 before, 11.00 after") {
		t.Errorf("the load average is not in the report:\n%s", section(out, "| **Run at**", "##"))
	}
	if !strings.Contains(out, "the machine was busy") {
		t.Errorf("a load of 14.5 against a 6-thread sweep was not called out:\n%s", section(out, "| **Run at**", "##"))
	}
}

func TestReportStatesTheLoadWithoutWarningWhenTheMachineWasQuiet(t *testing.T) {
	r := sampleReport()
	r.Machine.CPUCores = 12
	r.Threads = 6
	r.Machine.LoadBefore = 0.4
	// 9.3 is what an idle machine actually reads DURING this sweep: six
	// worker threads plus the parent and ordinary noise. The first version of
	// this threshold was half the core count, which called that busy and
	// would have stamped the warning on every honest run.
	r.Machine.LoadAfter = 9.3

	out := renderToString(t, r)
	if !strings.Contains(out, "0.40 before, 9.30 after") {
		t.Errorf("the load average should be reported even when it is fine:\n%s", section(out, "| **Run at**", "##"))
	}
	if strings.Contains(out, "the machine was busy") {
		t.Errorf("the sweep's own load was reported as contention:\n%s", section(out, "| **Run at**", "##"))
	}
}

// A machine that reports no load average at all — no /proc — must not grow a
// row reading "0.00 before, 0.00 after", which states a measurement that was
// never taken.
func TestReportOmitsTheLoadRowWhenItCouldNotBeRead(t *testing.T) {
	r := sampleReport()
	r.Machine.LoadBefore = 0
	r.Machine.LoadAfter = 0

	if strings.Contains(renderToString(t, r), "Load average") {
		t.Error("an unreadable load average was rendered as a measurement")
	}
}

// streamingSection is the whole "Streaming vs batch" block, cadence subsection
// included, so a cadence assertion cannot accidentally pass on text from the
// speed table.
func streamingSection(out string) string {
	return section(out, "## Streaming vs batch", "## Thread scaling")
}

// The regression that produced all of this: the default preview companion was
// swapped for a more accurate streaming model, it scored well here because
// time to first token was the only liveness number the report had, and a user
// reported the preview "comes in chunks rather than continuously" the next
// day. A model can start instantly and then paint in lumps, and until these
// columns existed the report could not tell the two apart.
func TestStreamingSectionMeasuresCadenceAndNotJustFirstToken(t *testing.T) {
	out := streamingSection(renderReport(t))
	for _, want := range []string{"Preview cadence", "Updates", "Mean gap", "Longest gap", "Slowest chunk"} {
		if !strings.Contains(out, want) {
			t.Errorf("cadence table omits %q", want)
		}
	}
	for _, want := range []string{"| 53 |", "357 ms", "660 ms", "23 ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("cadence table omits the measured value %q", want)
		}
	}
	// A number the reader has to convert themselves is a number they will
	// read wrong: the gaps are audio-time, and the slowest call is not.
	if !strings.Contains(out, "audio time") {
		t.Error("cadence section does not say the gaps are audio time, not wall clock")
	}
	if !strings.Contains(out, "wall clock") {
		t.Error("cadence section does not say the slowest chunk is the one wall-clock figure")
	}
}

// A table of gap figures that leaves the reader to decide what counts as
// lumpy is how the last regression shipped. The report has to say it.
func TestReportCallsOutAPreviewThatArrivesInLumps(t *testing.T) {
	r := sampleReport()
	for i := range r.Results {
		if r.Results[i].Backend.Mode == "streaming" {
			r.Results[i].Model = "nemotron-streaming-en-560ms"
			r.Results[i].Updates = 26
			r.Results[i].MeanGapMS = 716
			r.Results[i].MaxGapMS = 1140
		}
	}
	out := streamingSection(renderToString(t, r))

	if !strings.Contains(out, "[!WARNING]") {
		t.Fatal("a 716 ms mean gap raises no warning; it is reported as a bare number")
	}
	if !strings.Contains(out, "in lumps") {
		t.Error("the warning does not say what a 716 ms mean gap means: a preview arriving in lumps")
	}
	if !strings.Contains(out, "nemotron-streaming-en-560ms") {
		t.Error("the lumpy-preview warning does not name the model it is about")
	}
	if !strings.Contains(out, "716 ms") {
		t.Error("the lumpy-preview warning does not quote the mean gap it is based on")
	}

	// And the converse, which matters just as much: a model that paints
	// smoothly must not be warned about, or the warning stops being read.
	if smooth := streamingSection(renderReport(t)); strings.Contains(smooth, "[!WARNING]") {
		t.Error("a 357 ms mean gap is flagged as lumpy; that is the cadence of the companion nobody has complained about")
	}
}

// A FeedChunk longer than the daemon's tick is not a slow row, it is a model
// the daemon cannot drive: the preview goroutine is still in the recognizer
// when the next chunk is due, so audio queues up behind it.
func TestReportFlagsAFeedChunkThatOverrunsTheDaemonsTick(t *testing.T) {
	r := sampleReport()
	r.StreamChunkMS = 30
	for i := range r.Results {
		if r.Results[i].Backend.Mode == "streaming" {
			r.Results[i].SlowestChunkMS = 504
			r.Results[i].SlowChunks = 12
		}
	}
	out := streamingSection(renderToString(t, r))

	if !strings.Contains(out, "[!CAUTION]") {
		t.Fatal("a 504 ms FeedChunk against a 30 ms tick raises no warning")
	}
	if !strings.Contains(out, "**504 ms**") {
		t.Error("the overrunning cell is not marked in the table, so the warning cannot be traced to a row")
	}
	if !strings.Contains(out, "backs up") {
		t.Error("the warning does not say what an overrun does to the daemon")
	}

	// The sample's 23 ms worst call is inside the tick and must stay unmarked.
	clean := sampleReport()
	clean.StreamChunkMS = 30
	if got := streamingSection(renderToString(t, clean)); strings.Contains(got, "[!CAUTION]") {
		t.Error("a 23 ms worst call is flagged against a 30 ms tick; the flag would then fire on every model")
	}
}

// A streaming row that never produced a mid-stream update has no intervals to
// average, and a table of dashes reads as a measurement the harness forgot.
// It is the worst cadence there is and has to be named as one.
func TestCadenceSectionSaysSoWhenThePreviewNeverUpdated(t *testing.T) {
	r := sampleReport()
	for i := range r.Results {
		if r.Results[i].Backend.Mode == "streaming" {
			r.Results[i].Updates = 0
			r.Results[i].MeanGapMS = 0
			r.Results[i].MaxGapMS = 0
		}
	}
	out := streamingSection(renderToString(t, r))
	if !strings.Contains(out, "### Preview cadence") {
		t.Fatal("the cadence subsection vanished when no model updated")
	}
	if !strings.Contains(out, "Not measured") {
		t.Error("a cadence section with nothing to report does not say so")
	}
}

// The table carries what a reader can act on; the JSON carries everything,
// including the count of overrunning calls that tells a diagnostician whether
// it was one hiccup or a model that can never keep up.
func TestCadenceJSONCarriesTheFieldsTheTableLeavesOut(t *testing.T) {
	r := sampleReport()
	for i := range r.Results {
		if r.Results[i].Backend.Mode == "streaming" {
			r.Results[i].SlowChunks = 12
		}
	}
	path := filepath.Join(t.TempDir(), "out.json")
	if err := writeJSON(path, r); err != nil {
		t.Fatalf("writeJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"stream_updates", "stream_mean_gap_ms", "stream_max_gap_ms",
		"stream_slowest_chunk_ms", "stream_slow_chunks",
	} {
		if !strings.Contains(string(data), want) {
			t.Errorf("JSON omits %q, so a future run cannot be diffed on cadence", want)
		}
	}
}
