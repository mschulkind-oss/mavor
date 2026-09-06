//go:build !race

package overlay

// raceEnabled reports whether the race detector is instrumenting this build.
// A wall-clock assertion measures the instrumentation rather than the code
// when it is, so the frame-budget test skips instead of reporting a number
// that means nothing.
const raceEnabled = false
