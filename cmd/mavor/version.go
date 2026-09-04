package main

import (
	"fmt"
	"runtime"
)

// Build metadata. Commit and BuildTime are injected at link time by the
// Justfile's -ldflags and stay "unknown" under a plain `go build`.
var (
	Version   = "v0.1.0"
	Commit    = "unknown"
	BuildTime = "unknown"
	BuildTags = defaultBuildTags()
)

// versionString reports the running binary's identity. Every clone of this
// repo installs to the same ~/.local/bin/mavor, so the commit is what tells you
// which tree actually produced the binary you are running.
func versionString() string {
	return fmt.Sprintf("mavor %s (%s, built %s, %s/%s, %s, tags: %s)",
		Version, Commit, BuildTime, runtime.GOOS, runtime.GOARCH, runtime.Version(), BuildTags)
}

func runVersion() error {
	fmt.Println(versionString())
	return nil
}
