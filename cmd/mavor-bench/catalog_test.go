package main

import "testing"

func TestSelectModelsSplitsInstalledFromAbsent(t *testing.T) {
	c := &catalog{Models: []catalogModel{
		{Name: "tiny.en", Installed: true},
		{Name: "large-v3", Installed: false},
		{Name: "parakeet", Installed: true},
	}}
	selected, missing := selectModels(c, nil)
	if len(selected) != 2 {
		t.Errorf("selected %d models, want 2", len(selected))
	}
	if len(missing) != 1 || missing[0].Name != "large-v3" {
		t.Errorf("missing = %v, want just large-v3", missing)
	}
}

func TestSelectModelsNeverDownloadsWhatIsAbsent(t *testing.T) {
	// An uninstalled model must come back as missing even when named
	// explicitly. A benchmark run that quietly pulled 16 GB because a name
	// was on the command line would be a nasty surprise.
	c := &catalog{Models: []catalogModel{{Name: "large-v3", Installed: false}}}
	selected, missing := selectModels(c, []string{"large-v3"})
	if len(selected) != 0 {
		t.Errorf("selected %v, want nothing — the model is not downloaded", selected)
	}
	if len(missing) != 1 {
		t.Errorf("missing = %v, want large-v3 reported as absent", missing)
	}
}

func TestSelectModelsFiltersByNameAndAlias(t *testing.T) {
	c := &catalog{Models: []catalogModel{
		{Name: "tiny.en", Installed: true},
		{Name: "base.en", Installed: true},
		{Name: "parakeet", Aliases: []string{"parakeet-tdt"}, Installed: true},
	}}
	selected, _ := selectModels(c, []string{"base.en"})
	if len(selected) != 1 || selected[0].Name != "base.en" {
		t.Errorf("filtering by name gave %v, want just base.en", selected)
	}
	// An alias selects the model it belongs to, so a name that works for
	// `mavor models pull` works here too.
	selected, _ = selectModels(c, []string{"parakeet-tdt"})
	if len(selected) != 1 || selected[0].Name != "parakeet" {
		t.Errorf("filtering by alias gave %v, want parakeet", selected)
	}
}

func TestSelectModelsWithNoFilterTakesEverythingInstalled(t *testing.T) {
	// This is what makes the harness catalog-driven: a model added to the
	// catalog is benchmarked without anyone editing a list here.
	c := &catalog{Models: []catalogModel{
		{Name: "a", Installed: true}, {Name: "b", Installed: true}, {Name: "c", Installed: true},
	}}
	selected, missing := selectModels(c, nil)
	if len(selected) != 3 || len(missing) != 0 {
		t.Errorf("selected %d, missing %d; want all 3 selected", len(selected), len(missing))
	}
}

func TestBackendLabelDistinguishesBuildsAndModes(t *testing.T) {
	cases := []struct {
		b    backend
		want string
	}{
		{backend{Engine: "whisper-cli", Device: "cpu", Build: "stock", Mode: "batch"}, "whisper-cli / cpu (stock)"},
		{backend{Engine: "whisper-cli", Device: "gpu", Build: "vulkan", Mode: "batch"}, "whisper-cli / gpu (vulkan)"},
		{backend{Engine: "sherpa", Device: "cpu", Mode: "batch"}, "sherpa / cpu"},
		{backend{Engine: "sherpa", Device: "cpu", Mode: "streaming"}, "sherpa / cpu / streaming"},
	}
	for _, c := range cases {
		if got := c.b.label(); got != c.want {
			t.Errorf("label() = %q, want %q", got, c.want)
		}
	}
}

func TestHasGPUBackendRejectsACPUOnlyBuild(t *testing.T) {
	// The check that stops a CPU measurement being published as a GPU one.
	if hasGPUBackend([]string{"CPU"}) {
		t.Error("a CPU-only backend list was accepted as GPU-capable")
	}
	if hasGPUBackend(nil) {
		t.Error("an empty backend list was accepted as GPU-capable")
	}
	for _, b := range []string{"Vulkan", "CUDA", "Metal", "ROCm"} {
		if !hasGPUBackend([]string{"CPU", b}) {
			t.Errorf("backend list containing %q was not recognized as GPU-capable", b)
		}
	}
}
