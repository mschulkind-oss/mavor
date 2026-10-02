package overlay

import "testing"

func TestLifecycleDiagnosticsArePainted(t *testing.T) {
	for _, v := range []Visual{Visual(4), Visual(5), Error} {
		if v.String() == "unknown" {
			t.Fatalf("lifecycle visual %d is unknown", v)
		}
		_, plain, err := SceneSize(Scene{Visual: v})
		if err != nil {
			t.Fatal(err)
		}
		_, diagnostic, err := SceneSize(Scene{Visual: v, Preview: "Controlled fixture diagnostic"})
		if err != nil {
			t.Fatal(err)
		}
		if diagnostic <= plain {
			t.Fatalf("%v dropped diagnostic", v)
		}
	}
}

func TestX11LifecycleSetText(t *testing.T) {
	o := &X11{wake: make(chan struct{}, 1)}
	for _, v := range []Visual{Initializing, Error, Degraded} {
		if err := o.Show(v); err != nil {
			t.Fatal(err)
		}
		if err := o.SetText("diagnostic"); err != nil {
			t.Fatal(err)
		}
		if o.want.preview != "diagnostic" {
			t.Fatalf("%v backend dropped diagnostic", v)
		}
	}
}

func TestAdditiveLifecycleEnumsAndFixedCanvas(t *testing.T) {
	for i, v := range []Visual{Hidden, Recording, Transcribing, Error, Initializing, Degraded} {
		if int(v) != i {
			t.Fatal("visual enum renumbered", v)
		}
	}
	w, h, err := FixedSurfaceSize(100)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []Visual{Initializing, Error, Degraded} {
		s := Scene{Visual: v, Preview: "Controlled diagnostic", MaxPreviewWidth: 100, SurfaceW: w, SurfaceH: h}
		img, err := Render(s)
		if err != nil {
			t.Fatal(err)
		}
		bounds, err := SceneBounds(s)
		if err != nil {
			t.Fatal(err)
		}
		if !bounds.In(img.Bounds()) || !paintedBounds(img).In(bounds) {
			t.Fatalf("%v diagnostic clipped", v)
		}
	}
}
