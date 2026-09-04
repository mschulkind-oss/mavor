//go:build !nogtk && cgo

package main

func defaultBuildTags() string {
	return "cgo,gtk4,layer-shell"
}
