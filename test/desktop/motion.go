package desktop

import (
	"fmt"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/png"
	"os"
	"path/filepath"
	"strings"
)

func validateMotion(evidence []Evidence) error {
	phases := []string{"quiet", "speech", "pause", "recovery", "decayed"}
	var motion []Evidence
	for _, e := range evidence {
		if strings.HasPrefix(e.ID, "motion-") {
			motion = append(motion, e)
		}
	}
	if len(motion) != len(phases) {
		return fmt.Errorf("missing timed motion phases")
	}
	for i, e := range motion {
		if e.ID != "motion-"+phases[i] || e.CaptureStarted.IsZero() || e.CaptureEnded.Before(e.CaptureStarted) || (i > 0 && !e.CaptureStarted.After(motion[i-1].CaptureEnded)) {
			return fmt.Errorf("invalid motion capture order/timing: %s", e.ID)
		}
		levels := e.Receipt.Scene.Levels
		if len(levels) != 46 {
			return fmt.Errorf("missing production history: %s", e.ID)
		}
		min, max := 1., 0.
		for _, lv := range levels {
			if lv < min {
				min = lv
			}
			if lv > max {
				max = lv
			}
		}
		switch phases[i] {
		case "quiet", "decayed":
			if max != 0 {
				return fmt.Errorf("quiet/decay not at baseline: %s", e.ID)
			}
		case "speech", "pause", "recovery":
			if min != 0 || max < .2 {
				return fmt.Errorf("timed phase lost changing history: %s min=%v max=%v", e.ID, min, max)
			}
		}
		latest := levels[len(levels)-1]
		if phases[i] == "pause" && latest != 0 {
			return fmt.Errorf("pause live edge not quiet")
		}
		if (phases[i] == "speech" || phases[i] == "recovery") && latest < .2 {
			return fmt.Errorf("speech/recovery live edge absent")
		}
	}
	return nil
}

// saveMotionGIF converts only real lossless compositor captures into a labeled
// sparse sequence. Palette conversion is for GIF transport, not HUD generation.
func saveMotionGIF(dir, prefix string, evidence []Evidence) error {
	var frames []Evidence
	for _, e := range evidence {
		if strings.HasPrefix(e.ID, "motion-") {
			frames = append(frames, e)
		}
	}
	animation := &gif.GIF{LoopCount: 0}
	for i, e := range frames {
		f, err := os.Open(filepath.Join(dir, e.File))
		if err != nil {
			return err
		}
		img, err := png.Decode(f)
		_ = f.Close()
		if err != nil {
			return err
		}
		crop := cropTop(img, 180)
		frame := image.NewPaletted(image.Rect(0, 0, crop.Bounds().Dx(), 180), palette.Plan9)
		draw.FloydSteinberg.Draw(frame, frame.Bounds(), crop, crop.Bounds().Min)
		delay := 100 // final display hold, explicitly disclosed in the report
		if i+1 < len(frames) {
			delay = int(frames[i+1].CaptureEnded.Sub(e.CaptureEnded).Milliseconds() / 10)
			if delay < 1 {
				return fmt.Errorf("invalid GIF timing")
			}
		}
		animation.Image = append(animation.Image, frame)
		animation.Delay = append(animation.Delay, delay)
	}
	f, err := os.Create(filepath.Join(dir, prefix+"-motion.gif"))
	if err != nil {
		return err
	}
	err = gif.EncodeAll(f, animation)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
