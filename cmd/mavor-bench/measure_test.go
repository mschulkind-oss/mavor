package main

import (
	"math"
	"testing"
)

func TestWordErrorRateScoresRecognitionNotFormatting(t *testing.T) {
	const ref = "Lux is in the pit. He cannot sit still."

	// Casing and punctuation differences are not recognition errors.
	if got := wordErrorRate(ref, "lux is in the pit he cannot sit still"); got != 0 {
		t.Errorf("WER for a correctly recognized but unformatted transcript = %v, want 0", got)
	}
	// One substituted word out of nine.
	if got := wordErrorRate(ref, "Lux is in the pot. He cannot sit still."); math.Abs(got-1.0/9.0) > 1e-9 {
		t.Errorf("WER for one substitution = %v, want %v", got, 1.0/9.0)
	}
	// A dropped word is an error too.
	if got := wordErrorRate(ref, "Lux is in the pit. He cannot sit."); math.Abs(got-1.0/9.0) > 1e-9 {
		t.Errorf("WER for one deletion = %v, want %v", got, 1.0/9.0)
	}
}

func TestWordErrorRateIsNotCappedAtOne(t *testing.T) {
	// Hallucination is the failure mode that matters most in a dictation
	// tool, and clamping it to 1.0 would make a model that invents a
	// paragraph indistinguishable from one that returns nothing useful.
	got := wordErrorRate("hello there", "hello there and then a great deal more text nobody said at all")
	if got <= 1.0 {
		t.Errorf("WER for a hallucinated transcript = %v, want > 1.0", got)
	}
}

func TestWordErrorRateHandlesEmptyInput(t *testing.T) {
	if got := wordErrorRate("", ""); got != 0 {
		t.Errorf("WER of empty against empty = %v, want 0", got)
	}
	if got := wordErrorRate("", "spurious"); got != 1 {
		t.Errorf("WER of output against an empty reference = %v, want 1", got)
	}
	if got := wordErrorRate("a reference", ""); got != 1 {
		t.Errorf("WER of an empty transcript = %v, want 1 (everything deleted)", got)
	}
}

func TestNormalizeKeepsApostrophesAndDropsPunctuation(t *testing.T) {
	got := normalizeForWER("Don't — really! — stop.")
	want := []string{"don't", "really", "stop"}
	if len(got) != len(want) {
		t.Fatalf("normalizeForWER = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeForWER = %q, want %q", got, want)
		}
	}
}

func TestCharacterErrorRateSeparatesMisspellingFromDeletion(t *testing.T) {
	const ref = "the quick brown fox"
	misspelled := characterErrorRate(ref, "the quikc brown fox")
	deleted := characterErrorRate(ref, "the brown fox")
	if misspelled == 0 {
		t.Error("CER for a misspelling is 0; it should see the character difference")
	}
	if deleted <= misspelled {
		t.Errorf("CER for a dropped word (%v) should exceed CER for a misspelling (%v)", deleted, misspelled)
	}
}

func TestPunctuationDensityDistinguishesBareWordsFromProse(t *testing.T) {
	if got := punctuationDensity("lux is in the pit he cannot sit still"); got != 0 {
		t.Errorf("punctuation density of unpunctuated text = %v, want 0", got)
	}
	if got := punctuationDensity("Lux is in the pit. He cannot sit still."); got <= 0 {
		t.Errorf("punctuation density of punctuated text = %v, want > 0", got)
	}
	if got := punctuationDensity(""); got != 0 {
		t.Errorf("punctuation density of empty text = %v, want 0", got)
	}
}

func TestCapitalizationF1CatchesTheModelThatCapitalizesNothing(t *testing.T) {
	const ref = "Lux hops up. Then Jeremy runs."
	if got := capitalizationF1(ref, "lux hops up. then jeremy runs."); got != 0 {
		t.Errorf("capitalization F1 for all-lowercase output = %v, want 0", got)
	}
	if got := capitalizationF1(ref, ref); got != 1 {
		t.Errorf("capitalization F1 for identical text = %v, want 1", got)
	}
	// Partial credit: gets Lux, misses Jeremy.
	partial := capitalizationF1(ref, "Lux hops up. then jeremy runs.")
	if partial <= 0 || partial >= 1 {
		t.Errorf("capitalization F1 for partially capitalized output = %v, want strictly between 0 and 1", partial)
	}
}

func TestMedianResistsASingleOutlier(t *testing.T) {
	// The reason the harness reports a median: one stalled run out of three
	// must not move the published number.
	if got := median([]float64{100, 102, 5000}); got != 102 {
		t.Errorf("median with one outlier = %v, want 102", got)
	}
	if got := median([]float64{100, 200}); got != 150 {
		t.Errorf("median of an even count = %v, want 150", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("median of nothing = %v, want 0", got)
	}
}

func TestMedianDoesNotMutateItsInput(t *testing.T) {
	xs := []float64{3, 1, 2}
	median(xs)
	if xs[0] != 3 || xs[1] != 1 || xs[2] != 2 {
		t.Errorf("median reordered its caller's slice: %v", xs)
	}
}
