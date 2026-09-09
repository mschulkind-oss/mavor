package speech

import (
	"regexp"
	"strings"
)

// nonSpeechSpan matches the annotations Whisper models emit for audio that is
// not speech: "[BLANK_AUDIO]", "[MUSIC]", "(machine whirring)", "*coughs*".
//
// These are not an artifact of whisper.cpp — the binary contains no such
// literal. They come from the model, because the transcripts it was trained on
// annotated non-speech that way, so near-silence decodes to a marker rather
// than to nothing at all.
//
// Each alternative stops at its own closing delimiter, so nesting is not
// handled and does not need to be: models emit these flat.
var nonSpeechSpan = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*`)

// spaceBeforePunct matches the gap a removed span leaves in front of
// punctuation, so "hello (typing), world" does not become "hello , world".
var spaceBeforePunct = regexp.MustCompile(`[ \t]+([,.!?;:])`)

// horizontalRun matches a run of spaces or tabs — but never a newline, so a
// multi-segment transcript keeps its lines instead of being reflowed.
var horizontalRun = regexp.MustCompile(`[ \t]+`)

// StripNonSpeech removes non-speech annotations from a transcript and tidies
// the whitespace their removal leaves behind. A transcript that was nothing but
// annotations comes back empty, which the daemon already treats as "say
// nothing" — that is the whole point of the empty check it feeds.
//
// The cut is deliberately blunt: every bracketed, parenthesised or asterisked
// span goes, wherever it sits. A dictated parenthetical is collateral, and that
// trade was made on purpose — the markers are frequent and always wrong, the
// parentheticals are rare and recoverable from `mavor history`.
func StripNonSpeech(s string) string {
	if s == "" {
		return ""
	}
	s = nonSpeechSpan.ReplaceAllString(s, "")
	s = spaceBeforePunct.ReplaceAllString(s, "$1")

	// Rebuild line by line: drop the lines a removed marker emptied, keep the
	// lines that still hold speech.
	lines := strings.Split(s, "\n")
	kept := lines[:0]
	for _, line := range lines {
		line = strings.TrimSpace(horizontalRun.ReplaceAllString(line, " "))
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}
