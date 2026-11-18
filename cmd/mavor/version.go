package main

import (
	"fmt"
	"runtime"
)

// Build metadata, injected at link time: the Justfile's -ldflags for a local
// build, goreleaser's for a release. Under a plain `go build` none of it is
// set, and the honest reading is "dev / unknown" rather than the number of a
// release this binary is not.
var (
	Version   = "dev"
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
