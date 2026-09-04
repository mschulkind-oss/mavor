//go:build sherpa

package main

// defaultBuildTags describes the variant of the binary. A sherpa build links
// the in-process ONNX recognizers, which is the one thing that still needs cgo.
func defaultBuildTags() string {
	return "wayland,layer-shell,sherpa,cgo"
}
