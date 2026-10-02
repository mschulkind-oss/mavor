package models

import (
	"fmt"
	"strings"
)

// FinalMode selects when the main recognizer processes audio. Live modes are
// opt-in prototypes; only their finalized result can be emitted after release.
type FinalMode string

const (
	FinalAfterStop FinalMode = "after-stop"
	FinalStreaming FinalMode = "streaming"
	FinalSegments  FinalMode = "segments"
)

// FinalCapabilities separates intrinsic native streaming from mavor's supported
// final execution modes. Eligibility is not a quality or speed guarantee.
type FinalCapabilities struct {
	NativeStreaming     bool
	StreamingFinal      bool
	IncrementalSegments bool
}

// FinalCapabilities derives eligibility from catalog identity and runtime flags.
func (m KnownModel) FinalCapabilities() FinalCapabilities {
	return FinalCapabilities{
		NativeStreaming:     m.Streaming,
		StreamingFinal:      m.Engine == "sherpa" && m.Streaming,
		IncrementalSegments: m.Name == "parakeet-tdt-0.6b-v2" && m.Engine == "sherpa" && !m.Streaming,
	}
}

// FinalCapabilitiesFor never guesses identity from a path or a legacy alias.
func FinalCapabilitiesFor(name string) (FinalCapabilities, bool) {
	m, ok := Lookup(name)
	if !ok {
		return FinalCapabilities{}, false
	}
	return m.FinalCapabilities(), true
}

// ParseFinalMode defaults only an empty value; all other values match exactly.
func ParseFinalMode(value string) (FinalMode, error) {
	switch FinalMode(value) {
	case "", FinalAfterStop:
		return FinalAfterStop, nil
	case FinalStreaming:
		return FinalStreaming, nil
	case FinalSegments:
		return FinalSegments, nil
	default:
		return "", fmt.Errorf("unknown final mode %q; choose after-stop, streaming, segments", value)
	}
}

// Supports reports catalog eligibility, not loaded-backend compatibility.
func (c FinalCapabilities) Supports(mode FinalMode) bool {
	switch mode {
	case FinalAfterStop:
		return true
	case FinalStreaming:
		return c.StreamingFinal
	case FinalSegments:
		return c.IncrementalSegments
	default:
		return false
	}
}

// Modes returns supported modes in stable default-first order.
func (c FinalCapabilities) Modes() []FinalMode {
	modes := []FinalMode{FinalAfterStop}
	if c.StreamingFinal {
		modes = append(modes, FinalStreaming)
	}
	if c.IncrementalSegments {
		modes = append(modes, FinalSegments)
	}
	return modes
}

// ValidateFinalSelection permits unknown/custom identities only after stop.
// Runtime startup must also check the actual loaded backend and model layout.
func ValidateFinalSelection(name string, mode FinalMode) error {
	c, known := FinalCapabilitiesFor(name)
	if c.Supports(mode) && (known || mode == FinalAfterStop) {
		return nil
	}
	var choices []string
	for _, m := range c.Modes() {
		choices = append(choices, string(m))
	}
	return fmt.Errorf("model %q does not support final mode %q; supported modes: %s", name, mode, strings.Join(choices, ", "))
}
