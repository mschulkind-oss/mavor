package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/mschulkind-oss/mavor/internal/overlay"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"time"
)

type Evidence struct {
	ID             string                 `json:"id"`
	File           string                 `json:"file"`
	Receipt        overlay.FrameReceipt   `json:"receipt"`
	Candidates     []overlay.FrameReceipt `json:"candidates,omitempty"`
	CaptureStarted time.Time              `json:"capture_started"`
	CaptureEnded   time.Time              `json:"capture_ended"`
	Bounds         image.Rectangle        `json:"screenshot_bounds"`
}

// Drive feeds a full constant-RMS window, acknowledging each scheduled sample.
// Steady waveform levels make submission-to-capture animation harmless without
// freezing production behavior. The six levels still exercise the real RMS map.
func Drive(ctx context.Context, o overlay.Overlay, s StoryState) (overlay.FrameReceipt, error) {
	observer, ok := o.(overlay.FrameObserver)
	if !ok {
		return overlay.FrameReceipt{}, fmt.Errorf("real frame observer required")
	}
	if err := o.Show(s.Visual); err != nil {
		return overlay.FrameReceipt{}, err
	}
	if err := o.SetText(s.Preview); err != nil {
		return overlay.FrameReceipt{}, err
	}
	n := s.FeedFrames
	var r overlay.FrameReceipt
	for i := 0; i < n; i++ {
		if err := o.SetLevel(s.AudioLevel); err != nil {
			return r, err
		}
		var err error
		r, err = observer.SyncFrame(ctx)
		if err != nil {
			return r, err
		}
		time.Sleep(40 * time.Millisecond)
	}
	return observer.SyncFrame(ctx)
}

func BuildReport(dir, prefix, compositor, method, layout string, evidence []Evidence) error {
	states := Scenes()
	if len(evidence) != len(states) {
		return fmt.Errorf("expected %d catalog captures, got %d", len(states), len(evidence))
	}
	read := func(path string) ([]byte, image.Image, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		img, err := png.Decode(bytes.NewReader(raw))
		return raw, img, err
	}
	_, base, err := read(evidence[0].File)
	if err != nil {
		return err
	}
	var captures []StateCapture
	for i, s := range states {
		e := &evidence[i]
		if i > 0 && (e.Receipt.Frame <= evidence[i-1].Receipt.Frame || e.Receipt.Revision <= evidence[i-1].Receipt.Revision) {
			return fmt.Errorf("stale/reused submission receipt %s", s.ID)
		}
		if e.ID != s.ID || e.Receipt.Scene.Visual != s.Visual || e.Receipt.Scene.Preview != s.Preview {
			return fmt.Errorf("scene/receipt mismatch %s", s.ID)
		}
		raw, img, err := read(e.File)
		if err != nil {
			return err
		}
		if s.Motion == "" {
			e.Bounds, err = Validate(base, img, e.Receipt)
		} else {
			// Match actual compositor pixels against bounded real production submissions.
			// No shifted/synthesized reference and no enlarged waveform tolerance.
			err = fmt.Errorf("no real submission matched timed capture")
			for _, r := range e.Candidates {
				if r.SubmittedAt.IsZero() || r.Frame < e.Receipt.Frame || r.Revision < e.Receipt.Revision || r.Scene.Visual != s.Visual || r.Scene.Preview != s.Preview || r.SubmittedAt.After(e.CaptureEnded) {
					continue
				}
				bounds, matchErr := Validate(base, img, r)
				if matchErr == nil {
					e.Receipt = r
					e.Bounds = bounds
					err = nil
					break
				}
			}
		}
		if err != nil {
			return fmt.Errorf("%s: %w", s.ID, err)
		}
		if s.Visual == overlay.Recording && s.Motion == "" {
			expected := 0.
			if s.AudioLevel > 0 {
				expected = (20*math.Log10(s.AudioLevel) + 50) / 50
			}
			max := 0.
			for _, v := range e.Receipt.Scene.Levels {
				if v > max {
					max = v
				}
			}
			if math.Abs(max-expected) > .00001 {
				return fmt.Errorf("incorrect level receipt")
			}
		}
		s.Specs = map[string]string{"Capture": method, "Submission backend": e.Receipt.Backend, "Submission revision": fmt.Sprint(e.Receipt.Revision), "Measured screenshot canvas": fmt.Sprint(e.Bounds), "X protocol bounds (not screenshot space)": fmt.Sprint(e.Receipt.Screen), "Production scale": fmt.Sprint(e.Receipt.Scale), "Preview": s.Preview, "Raw RMS": fmt.Sprint(s.AudioLevel), "Waveform": "Steady RMS, 50 scheduled frames; decibel mapped"}
		if s.Motion != "" {
			s.Specs["Waveform"] = "Continuous production ring, timed controlled RMS fixture (not a microphone)"
			s.Specs["Capture started"] = e.CaptureStarted.Format(time.RFC3339Nano)
			s.Specs["Capture ended"] = e.CaptureEnded.Format(time.RFC3339Nano)
		}
		c, err := SaveCapture(dir, prefix, s, raw)
		if err != nil {
			return err
		}
		e.File = filepath.Join(prefix+"-screenshots", c.FullFileName)
		captures = append(captures, c)
	}
	if err := validateMotion(evidence); err != nil {
		return err
	}
	if err := saveMotionGIF(dir, prefix, evidence); err != nil {
		return err
	}
	manifest, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, prefix+".json"), manifest, 0644); err != nil {
		return err
	}
	return GenerateHTMLReport(filepath.Join(dir, prefix+".html"), ReportData{GeneratedAt: "(fixed for reproducible output)", TotalStates: len(states), Compositor: compositor, CaptureMethod: method, Layout: layout, DisplayRes: "1920x1080", Captures: captures, MotionFile: prefix + "-motion.gif"})
}

// ValidateArtifact applies the same screenshot oracle to extra lifecycle captures.
func ValidateArtifact(baseline string, e Evidence) error {
	open := func(path string) (image.Image, error) {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		return png.Decode(f)
	}
	base, err := open(baseline)
	if err != nil {
		return err
	}
	actual, err := open(e.File)
	if err != nil {
		return err
	}
	_, err = Validate(base, actual, e.Receipt)
	return err
}
