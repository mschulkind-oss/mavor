package main

import (
	"context"
	"fmt"
)

// starter is the optional interface a transcriber implements when it has a
// model or a subprocess to bring up before it can decode.
type starter interface {
	Start(context.Context) error
}

// beginStart brings a transcriber's engine up in the background and returns
// the function that waits for it.
//
// The main model and the preview companion are independent — the companion
// never influences the transcript and the main model never sees the preview —
// but they were loaded one after the other, so the wait before the daemon was
// usable was main + companion rather than max(main, companion). Both are
// multi-hundred-megabyte ONNX or GGML loads; on a cold cache that difference
// is the several seconds a user spends wondering whether the preview works.
//
// The returned wait function MUST be called on every path out, including
// error paths, before the transcriber is closed: it is what guarantees the
// background Start has finished touching it.
func beginStart(ctx context.Context, t any) func() error {
	s, ok := t.(starter)
	if !ok {
		return func() error { return nil }
	}

	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	return func() error {
		if err := <-done; err != nil {
			return fmt.Errorf("start transcriber engine: %w", err)
		}
		return nil
	}
}

// waitOrLog is beginStart's wait on an unwinding path, where the caller
// already has an error to return and only needs the goroutine to be finished.
func waitOrLog(wait func() error, log func(string, ...any)) {
	if err := wait(); err != nil {
		log("transcriber engine failed to start while shutting down", "err", err)
	}
}
