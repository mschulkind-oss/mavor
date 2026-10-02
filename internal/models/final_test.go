package models

import (
	"reflect"
	"strings"
	"testing"
)

func TestFinalCapabilitiesCatalog(t *testing.T) {
	for _, m := range Catalog {
		c, ok := FinalCapabilitiesFor(m.Name)
		want := FinalCapabilities{NativeStreaming: m.Streaming, StreamingFinal: m.Engine == "sherpa" && m.Streaming, IncrementalSegments: m.Name == "parakeet-tdt-0.6b-v2" && m.Engine == "sherpa" && !m.Streaming}
		if !ok || c != want || m.FinalCapabilities() != want {
			t.Errorf("%s: got %+v, want %+v", m.Name, c, want)
		}
		modes := []FinalMode{FinalAfterStop}
		if want.StreamingFinal {
			modes = append(modes, FinalStreaming)
		}
		if want.IncrementalSegments {
			modes = append(modes, FinalSegments)
		}
		if !reflect.DeepEqual(c.Modes(), modes) {
			t.Errorf("%s modes = %v", m.Name, c.Modes())
		}
		for _, mode := range []FinalMode{FinalAfterStop, FinalStreaming, FinalSegments, "invalid"} {
			supported := mode == FinalAfterStop || mode == FinalStreaming && want.StreamingFinal || mode == FinalSegments && want.IncrementalSegments
			if c.Supports(mode) != supported || (ValidateFinalSelection(m.Name, mode) == nil) != supported {
				t.Errorf("%s/%s eligibility mismatch", m.Name, mode)
			}
		}
	}
}

func TestFinalModesStrictParsingAndUnknownIdentity(t *testing.T) {
	for _, s := range []string{"", "after-stop", "streaming", "segments"} {
		got, err := ParseFinalMode(s)
		want := FinalMode(s)
		if s == "" {
			want = FinalAfterStop
		}
		if err != nil || got != want {
			t.Errorf("parse %q = %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"Streaming", " streaming", "segments ", "batch"} {
		_, err := ParseFinalMode(s)
		if err == nil || !strings.Contains(err.Error(), "after-stop, streaming, segments") {
			t.Errorf("parse %q: %v", s, err)
		}
	}
	for _, name := range []string{"missing", "/models/nemotron-streaming-en-560ms", "parakeet", "sherpa/parakeet-tdt-0.6b-v2"} {
		c, ok := FinalCapabilitiesFor(name)
		if ok || c != (FinalCapabilities{}) {
			t.Errorf("unknown %q guessed capabilities", name)
		}
		if err := ValidateFinalSelection(name, FinalAfterStop); err != nil {
			t.Fatal(err)
		}
		for _, mode := range []FinalMode{FinalStreaming, FinalSegments, "invalid"} {
			if ValidateFinalSelection(name, mode) == nil {
				t.Errorf("unknown %q accepted %s", name, mode)
			}
		}
	}
	// Eligibility checks the engine/layout flags too, not merely the name.
	for _, m := range []KnownModel{{Name: "parakeet-tdt-0.6b-v2", Engine: "whisper"}, {Name: "parakeet-tdt-0.6b-v2", Engine: "sherpa", Streaming: true}} {
		if m.FinalCapabilities().IncrementalSegments {
			t.Errorf("unsafe segment eligibility: %+v", m)
		}
	}
	if (KnownModel{Engine: "whisper", Streaming: true}).FinalCapabilities().StreamingFinal {
		t.Fatal("non-sherpa streaming final accepted")
	}
}
