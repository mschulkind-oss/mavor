package daemon

// dictationCycle is one key-down..emit run. Exactly one may be Recording; any
// number may await transcription. It owns the per-recording facts that used to
// be daemon fields when only one cycle could exist: the capture file, this
// recording's silence-filter evidence, the preview drain its final decode must
// await, and the provenance candidates the dispatcher commits in spoken order.
type dictationCycle struct {
	id  uint64
	wav string

	// hadSpeech is this recording's recognition evidence for the energy filter,
	// and drain closes when its preview recognizer (or companion finalization)
	// has returned. Guarded by d.streamMu. Both are per-cycle so a later
	// recording cannot leak evidence into an earlier cycle's silence decision.
	hadSpeech bool
	drain     chan struct{}
	// usesMain reports whether this recording's preview drove the main
	// recognizer, so its final decode must await drain before reusing it.
	usesMain bool
}
