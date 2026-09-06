package overlay

import (
	"image"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/wayland"
)

// A layer-shell resize is asynchronous: on the frame that asks for a new size
// the compositor has not acked it, so the surface is still the old, smaller
// one. Every part of the paint path has to survive that one frame.
//
// It did not. The scratch image was sized from the surface rather than the
// scene, RenderInto refused the undersized buffer, and the error stopped the
// render loop — the overlay vanished mid-recording while the daemon carried on
// recording behind it.

// The scratch buffer is sized from the scene, so it always fits.
func TestScratchSizedFromTheSceneAlwaysFits(t *testing.T) {
	// A preview appearing is exactly the moment the surface is stale: the
	// pill alone is ~329x56 and the capped strip takes it to 1280x91.
	scene := Scene{Visual: Recording, Preview: "hello", MaxPreviewWidth: 1280}
	w, h, err := SceneSize(scene)
	if err != nil {
		t.Fatalf("SceneSize: %v", err)
	}

	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := RenderInto(img, scene); err != nil {
		t.Fatalf("a buffer sized from the scene was refused: %v", err)
	}
}

// blit is what makes the stale frame survivable: it crops to whichever side is
// smaller rather than reading or writing past either.
func TestBlitCropsASceneLargerThanTheSurface(t *testing.T) {
	scene := Scene{Visual: Recording, Preview: "hello", MaxPreviewWidth: 1280}
	w, h, err := SceneSize(scene)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := RenderInto(img, scene); err != nil {
		t.Fatal(err)
	}

	// The surface as it still is on that frame: the old, smaller size.
	const sw, sh = 329, 56
	buf := &wayland.Buffer{Width: sw, Height: sh, Stride: sw * 4, Pix: make([]byte, sw*sh*4)}

	// Must not panic and must not write past the destination.
	blit(img, buf)

	if len(buf.Pix) != sw*sh*4 {
		t.Fatalf("blit resized the destination: %d bytes, want %d", len(buf.Pix), sw*sh*4)
	}

	// The frame is legitimately BLANK here, and that is worth stating rather
	// than asserting away: the pill is centred in the 1280px scene, so it
	// begins at x=475 and the crop to 329px lands entirely left of it. One
	// blank frame while the compositor acks the resize is the cost of a
	// stable width, and it is a great deal better than the render loop
	// stopping — which is what this whole file exists to prevent.
}

// Same frame, from the other side: a scene SMALLER than the surface must also
// blit safely, which is the case on the way back down when the preview clears.
func TestBlitHandlesASceneSmallerThanTheSurface(t *testing.T) {
	scene := Scene{Visual: Recording}
	w, h, err := SceneSize(scene)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	if err := RenderInto(img, scene); err != nil {
		t.Fatal(err)
	}

	const sw, sh = 1280, 91
	buf := &wayland.Buffer{Width: sw, Height: sh, Stride: sw * 4, Pix: make([]byte, sw*sh*4)}
	blit(img, buf)

	painted := false
	for _, v := range buf.Pix {
		if v != 0 {
			painted = true
			break
		}
	}
	if !painted {
		t.Error("a scene smaller than the surface blitted nothing")
	}
}
