//go:build nogtk || !cgo

package main

func defaultBuildTags() string {
	return "nogtk,headless"
}
