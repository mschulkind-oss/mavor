package main

// defaultBuildTags describes the variant of the binary. There is only one:
// mavor is a cgo program that links the in-process sherpa-onnx ONNX
// recognizers, and the overlay is pure Go regardless.
func defaultBuildTags() string {
	return "wayland,layer-shell,cgo"
}
