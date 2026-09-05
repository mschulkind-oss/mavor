package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseThreadSweepSortsAndDeduplicates(t *testing.T) {
	got, err := parseThreadSweep(" 8, 2,4 ,4, 6 ")
	if err != nil {
		t.Fatalf("parseThreadSweep: %v", err)
	}
	want := []int{2, 4, 6, 8}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestParseThreadSweepEmptyMeansOffNotError(t *testing.T) {
	got, err := parseThreadSweep("  ")
	if err != nil {
		t.Fatalf("an empty sweep is how the section is disabled, not an error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %v, want no thread counts", got)
	}
}

func TestParseThreadSweepRejectsNonsense(t *testing.T) {
	for _, spec := range []string{"2,zero", "0,4", "-2"} {
		if _, err := parseThreadSweep(spec); err == nil {
			t.Errorf("parseThreadSweep(%q) accepted a thread count that cannot be run", spec)
		}
	}
}

func TestSelectSweepModelsKeepsOrderAndSkipsWhatIsNotThere(t *testing.T) {
	installed := []catalogModel{
		{Name: "base.en", Engine: "whisper", Installed: true},
		{Name: "tiny.en", Engine: "whisper", Installed: true, Aliases: []string{"tiny-en"}},
		{Name: "parakeet", Engine: "sherpa", Installed: true},
	}
	got, absent := selectSweepModels(installed, []string{"tiny-en", "base.en", "small.en", "parakeet"})

	if len(got) != 2 || got[0].Name != "tiny.en" || got[1].Name != "base.en" {
		t.Fatalf("got %v, want tiny.en then base.en (the order asked for, aliases resolved)", got)
	}
	// Both a model that is not downloaded and one that is not whisper come
	// back as absent: the sweeps vary whisper.cpp settings sherpa does not
	// have, and a user who asked for either needs to be told it was skipped.
	if len(absent) != 2 || absent[0] != "small.en" || absent[1] != "parakeet" {
		t.Fatalf("got absent %v, want small.en and parakeet", absent)
	}
}

// renderSweepReport renders a report carrying both sweeps.
func renderSweepReport(t *testing.T) string {
	t.Helper()
	r := sampleReport()
	r.ThreadScaling = []threadCell{
		{Model: "base.en", Threads: 2, Runs: 3, TotalMS: 4000, RTF: 0.2, PeakRSSKB: 300 * 1024},
		{Model: "base.en", Threads: 4, Runs: 3, TotalMS: 2000, RTF: 0.1, PeakRSSKB: 300 * 1024},
		{Model: "small.en", Threads: 2, Failed: true, Error: "timed out"},
	}
	r.WarmServer = []serverCell{
		{Model: "base.en", Threads: 4, Runs: 3, StartupMS: 900, WarmMS: 1500, ColdMS: 2000, RTF: 0.075},
	}
	path := filepath.Join(t.TempDir(), "report.md")
	if err := writeMarkdown(path, r); err != nil {
		t.Fatalf("writeMarkdown: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestThreadScalingSectionShowsWhatTheCoresBought(t *testing.T) {
	out := renderSweepReport(t)
	sweep := section(out, "## Thread scaling", "## Warm server")
	if sweep == "" {
		t.Fatal("report has no thread scaling section")
	}
	if !strings.Contains(sweep, "2.00×") {
		t.Error("thread scaling omits the speedup against the model's slowest thread count")
	}
	// A failed cell says so rather than rendering a zero time.
	if !strings.Contains(sweep, "| `small.en` | 2 | failed") {
		t.Error("a failed sweep cell is missing or is rendered as if it had a time")
	}
}

func TestWarmServerSectionComparesAgainstTheColdCLI(t *testing.T) {
	out := renderSweepReport(t)
	warm := section(out, "## Warm server", "## Cells that failed")
	if warm == "" {
		t.Fatal("report has no warm server section")
	}
	if !strings.Contains(warm, "-500 ms") {
		t.Error("warm server section does not state what holding the model warm saved")
	}
	if !strings.Contains(warm, "900 ms") {
		t.Error("warm server section drops startup, which is the cost the saving is traded against")
	}
}

// An absent sweep must read as absent. A section that silently disappears is
// how a reader concludes the setting was never worth measuring.
func TestAbsentSweepsSayTheyAreAbsent(t *testing.T) {
	out := renderReport(t) // sampleReport carries neither sweep
	for _, heading := range []string{"## Thread scaling", "## Warm server vs cold CLI"} {
		if !strings.Contains(out, heading) {
			t.Errorf("report drops the %q section entirely when it has no cells", heading)
		}
	}
	if strings.Count(out, "**Not measured** in this run") != 2 {
		t.Error("an unmeasured sweep does not say it was not measured")
	}
}
