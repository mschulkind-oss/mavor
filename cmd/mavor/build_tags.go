//go:build !sherpa

package main

// defaultBuildTags describes the variant of the binary, which now turns only
// on whether the in-process sherpa engine is compiled in. The overlay is
// always present and always pure Go.
func defaultBuildTags() string {
	return "wayland,layer-shell"
}
