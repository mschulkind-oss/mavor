//go:build cgo && !nogtk

package overlay

import (
	"strings"
	"testing"
)

// The stylesheet is built by token substitution because its keyframes contain
// bare percent signs. Guard both halves: the padding must be filled in, and
// the keyframe percentages must survive untouched.
func TestStyleSheetSubstitutesPaddingWithoutManglingKeyframes(t *testing.T) {
	css := styleSheet()
	if !strings.Contains(css, "padding: 7px 22px;") {
		t.Errorf("padding not interpolated from the Go constants:\n%s", css[:400])
	}
	if strings.Contains(css, "$PAD_") {
		t.Error("stylesheet still carries an unsubstituted token")
	}
	for _, kf := range []string{"0%, 60%, 100%", "30%"} {
		if !strings.Contains(css, kf) {
			t.Errorf("keyframe stop %q was mangled", kf)
		}
	}
	if strings.Contains(css, "%!") {
		t.Error("stylesheet contains a format-verb error marker")
	}
}
