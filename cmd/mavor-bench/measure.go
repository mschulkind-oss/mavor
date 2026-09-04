package main

import (
	"strings"
	"unicode"
)

// Peak RSS is reported in kilobytes by both of the sources we read it from —
// getrusage(2) for subprocesses and /proc/self/status VmHWM for in-process
// work — so the whole harness carries kilobytes and converts only at render
// time. Mixing the two units is the easiest way to publish a memory figure
// that is wrong by 1024x, which is exactly the class of error this report
// exists to stop repeating.
const kbPerMB = 1024

// normalizeForWER lowercases, strips punctuation, and collapses whitespace.
// It is the standard ASR scoring normalization: a model that writes "Lux is"
// where the reference says "lux is." has not made a recognition error, and
// counting one would drown the real errors in formatting noise.
//
// Punctuation and casing are scored separately, on the raw text, by
// punctuationDensity and capitalizationF1 — normalizing here does not throw
// that information away, it just keeps it out of the word error rate.
func normalizeForWER(s string) []string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(unicode.ToLower(r))
		case r == '\'':
			// Kept: "dont" and "don't" are different words, and dropping the
			// apostrophe would silently merge them.
			b.WriteRune(r)
		default:
			b.WriteRune(' ')
		}
	}
	return strings.Fields(b.String())
}

// editDistance is Levenshtein over word slices, computed with two rows rather
// than a full matrix: the transcripts here are short, but the harness runs it
// once per model per backend and there is no reason to allocate n*m.
func editDistance(ref, hyp []string) int {
	if len(ref) == 0 {
		return len(hyp)
	}
	prev := make([]int, len(ref)+1)
	curr := make([]int, len(ref)+1)
	for i := range prev {
		prev[i] = i
	}
	for j := 1; j <= len(hyp); j++ {
		curr[0] = j
		for i := 1; i <= len(ref); i++ {
			cost := 1
			if ref[i-1] == hyp[j-1] {
				cost = 0
			}
			curr[i] = min(min(curr[i-1]+1, prev[i]+1), prev[i-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[len(ref)]
}

// wordErrorRate is edit distance over reference length. It is not capped at
// 1.0: a model that hallucinates a paragraph against a one-line reference
// genuinely scores above 100%, and clamping that to a tidy 1.0 would hide the
// single most important failure mode in a dictation tool.
func wordErrorRate(reference, hypothesis string) float64 {
	ref := normalizeForWER(reference)
	hyp := normalizeForWER(hypothesis)
	if len(ref) == 0 {
		if len(hyp) == 0 {
			return 0
		}
		return 1
	}
	return float64(editDistance(ref, hyp)) / float64(len(ref))
}

// characterErrorRate is the same measure over characters of the normalized
// text. It separates a model that misspells from one that drops whole words:
// both can score the same WER, and they are not the same failure.
func characterErrorRate(reference, hypothesis string) float64 {
	ref := strings.Split(strings.Join(normalizeForWER(reference), " "), "")
	hyp := strings.Split(strings.Join(normalizeForWER(hypothesis), " "), "")
	if len(ref) == 0 {
		if len(hyp) == 0 {
			return 0
		}
		return 1
	}
	return float64(editDistance(ref, hyp)) / float64(len(ref))
}

// punctuationDensity is punctuation marks per word in the raw transcript. It
// is not an accuracy score — it is how the report distinguishes a model that
// emits bare words from one that produces text you can paste into a document
// without editing it, which for a dictation tool is most of the value.
func punctuationDensity(text string) float64 {
	words := len(strings.Fields(text))
	if words == 0 {
		return 0
	}
	marks := 0
	for _, r := range text {
		if unicode.IsPunct(r) {
			marks++
		}
	}
	return float64(marks) / float64(words)
}

// capitalizationF1 scores capitalized words against the reference's, as
// balanced precision and recall. Proper nouns are where dictation most
// visibly fails, and a model that capitalizes nothing scores a clean 0 here
// while its WER stays perfect.
func capitalizationF1(reference, hypothesis string) float64 {
	capsOf := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.Fields(s) {
			trimmed := strings.TrimFunc(w, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
			if trimmed == "" {
				continue
			}
			if r := []rune(trimmed)[0]; unicode.IsUpper(r) {
				out[strings.ToLower(trimmed)] = true
			}
		}
		return out
	}
	ref, hyp := capsOf(reference), capsOf(hypothesis)
	if len(ref) == 0 && len(hyp) == 0 {
		return 1
	}
	hits := 0
	for w := range hyp {
		if ref[w] {
			hits++
		}
	}
	if hits == 0 {
		return 0
	}
	precision := float64(hits) / float64(len(hyp))
	recall := float64(hits) / float64(len(ref))
	return 2 * precision * recall / (precision + recall)
}

// median returns the middle value, averaging the two middle ones for an even
// count. The harness reports the median of its runs rather than the mean
// because a single scheduler stall or page-cache miss skews a mean of three
// runs badly, and rather than the minimum because the best case is not what a
// user experiences.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	mid := len(s) / 2
	if len(s)%2 == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / 2
}
