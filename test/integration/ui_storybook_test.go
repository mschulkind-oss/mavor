//go:build integration || e2e

package integration

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html/template"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/overlay"
)

type StoryState struct {
	ID          string
	Index       int
	Title       string
	Badge       string
	BadgeClass  string
	Description string
	Visual      overlay.Visual
	AudioLevel  float64 // 0.0 - 1.0 (used if Visual == overlay.Recording)
	LevelPct    int
	Specs       map[string]string
}

type StateCapture struct {
	State        StoryState
	FullB64      string
	CropB64      string
	FullFileName string
	CropFileName string
	FullRelPath  string
	CropRelPath  string
	Width        int
	Height       int
	CropHeight   int
	SizeBytes    int
	SizeKB       string
}

type ReportData struct {
	GeneratedAt  string
	TotalStates  int
	Compositor   string
	DisplayRes   string
	WaybarHeight int
	TopMargin    int
	Captures     []StateCapture
}

func cropTop(img image.Image, h int) image.Image {
	rect := image.Rect(0, 0, img.Bounds().Dx(), h)
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	if si, ok := img.(subImager); ok {
		return si.SubImage(rect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	for y := 0; y < h; y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			dst.Set(x, y, img.At(x, y))
		}
	}
	return dst
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// speechEnvelope is a 46-sample spoken-phrase pattern (normalized 0..1) the
// storybook feeds through SetLevel to fill the whole waveform window: three
// and a half words with inter-word gaps, newest sample at the live edge. It
// mirrors what the daemon's ~33 Hz level cadence pushes while someone talks,
// so the screenshots show a filled, scrolling waveform rather than a
// partial ramp.
var speechEnvelope = []float64{
	0.00, 0.15, 0.40, 0.70, 0.90, 1.00, 0.70, 0.40, 0.20, 0.10, // word 1
	0.05, 0.00, 0.00, 0.00, // gap
	0.05, 0.15, 0.40, 0.70, 0.90, 1.00, 0.80, 0.50, 0.30, 0.15, // word 2
	0.05, 0.00, 0.00, 0.00, // gap
	0.10, 0.30, 0.60, 0.85, 1.00, 0.80, 0.60, 0.40, 0.20, 0.10, // word 3
	0.05, 0.00, 0.00, // dip
	0.10, 0.35, 0.65, 0.85, 1.00, // word 4 attack (live edge)
}

// generatedAt returns the report's timestamp. It is fixed by default so
// regenerating the storybook produces a byte-identical HTML file and the
// committed report only changes when the UI does. Set MAVOR_STORYBOOK_STAMP=1 for
// a real wall-clock stamp.
func generatedAt() string {
	if os.Getenv("MAVOR_STORYBOOK_STAMP") == "1" {
		return time.Now().Format("2006-01-02 15:04:05 MST")
	}
	return "(fixed for reproducible output)"
}

func TestUIStorybookReport(t *testing.T) {
	const (
		dispWidth    = 1920
		dispHeight   = 1080
		waybarHeight = 32
		topMargin    = 8
		cropHeight   = 180
	)

	// Shared, not per-test: this test builds an overlay in-process, and the
	// GTK application it starts must not outlive its compositor. The shared
	// compositor is 1920x1080 with waybar, which is what this test wants.
	h := sharedCompositor(t)

	t.Setenv("XDG_RUNTIME_DIR", h.XDGRuntime)
	t.Setenv("WAYLAND_DISPLAY", h.WaylandDisp)
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", h.DBusAddr)
	t.Setenv("GDK_BACKEND", "wayland")

	// Freeze the pulsing dot and typing dots so captures are byte-reproducible;
	// otherwise every regeneration rewrites the PNGs at a different animation
	// phase and the committed report churns for no reason.
	t.Setenv("MAVOR_OVERLAY_STATIC", "1")

	ov, err := overlay.NewGTK(topMargin)
	if err != nil {
		t.Fatalf("overlay.NewGTK: %v", err)
	}
	defer ov.Close()

	states := []StoryState{
		{
			Index:       1,
			ID:          "hidden",
			Title:       "Hidden",
			Badge:       "IDLE / HIDDEN",
			BadgeClass:  "badge-hidden",
			Description: "Clean desktop baseline with top-anchored Waybar. The overlay layer-shell surface is unmapped and invisible.",
			Visual:      overlay.Hidden,
			AudioLevel:  0.0,
			LevelPct:    0,
			Specs: map[string]string{
				"Visual State": "Hidden",
				"Window State": "Unmapped (0x0)",
				"Audio Meter":  "Inactive",
				"Top Margin":   "8px",
			},
		},
		{
			Index:       2,
			ID:          "recording-00",
			Title:       "Recording (Silence / Noise Floor - 0%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Active voice capture in total silence or below VAD threshold. The time-scroll waveform sits at its 2px baseline; the right edge is always the newest sample, history scrolling left.",
			Visual:      overlay.Recording,
			AudioLevel:  0.0,
			LevelPct:    0,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "0% (0.00)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       3,
			ID:          "recording-15",
			Title:       "Recording (Subtle Whisper - 15%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Low microphone energy / soft whispering. A spoken phrase scrolls through the window at 15% amplitude; inter-word gaps read as baseline, live edge brightest.",
			Visual:      overlay.Recording,
			AudioLevel:  0.15,
			LevelPct:    15,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "15% (0.15)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       4,
			ID:          "recording-35",
			Title:       "Recording (Quiet Speech - 35%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Gentle voice input. A spoken phrase scrolls left at 35% amplitude — each column keeps its own height, gaps between words dip to baseline.",
			Visual:      overlay.Recording,
			AudioLevel:  0.35,
			LevelPct:    35,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "35% (0.35)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       5,
			ID:          "recording-55",
			Title:       "Recording (Conversational Speech - 55%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Standard conversational speech. Full-window phrase at 55% amplitude, fresh at the live edge.",
			Visual:      overlay.Recording,
			AudioLevel:  0.55,
			LevelPct:    55,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "55% (0.55)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       6,
			ID:          "recording-75",
			Title:       "Recording (Animated Speech - 75%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Strong emphasis / louder speech. Phrase peaks near the top of the canvas at 75% amplitude.",
			Visual:      overlay.Recording,
			AudioLevel:  0.75,
			LevelPct:    75,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "75% (0.75)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       7,
			ID:          "recording-100",
			Title:       "Recording (Peak Volume / Max - 100%)",
			Badge:       "RECORDING",
			BadgeClass:  "badge-recording",
			Description: "Maximum input signal before clipping. Word peaks reach full canvas height at 100% amplitude.",
			Visual:      overlay.Recording,
			AudioLevel:  1.00,
			LevelPct:    100,
			Specs: map[string]string{
				"Visual State": "Recording",
				"Audio Level":  "100% (1.00)",
				"Waveform":     "Time-scroll, 46 cols · ~1.4s @ 33Hz",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       8,
			ID:          "transcribing",
			Title:       "Transcribing",
			Badge:       "TRANSCRIBING",
			BadgeClass:  "badge-transcribing",
			Description: "Whisper inference in progress — no audio is being captured in this state, so no waveform is shown. Amber pill with 'TRANSCRIBING…' and a typing-dots indicator while the tail of the recording is transcribed.",
			Visual:      overlay.Transcribing,
			AudioLevel:  0.0,
			LevelPct:    0,
			Specs: map[string]string{
				"Visual State": "Transcribing",
				"Color Accent": "Amber Gradient (#d68910 → #7a4807)",
				"Indicator":    "Typing dots (no waveform — not recording)",
				"Layer":        "Top (Float below Waybar)",
			},
		},
		{
			Index:       9,
			ID:          "error",
			Title:       "Error",
			Badge:       "ERROR",
			BadgeClass:  "badge-error",
			Description: "Warning alert state. Deep crimson warning pill with '⚠ ERROR' warning icon and text before timeout return to idle.",
			Visual:      overlay.Error,
			AudioLevel:  0.0,
			LevelPct:    0,
			Specs: map[string]string{
				"Visual State": "Error",
				"Color Accent": "Crimson Gradient (#a80000 → #5c0000)",
				"Indicator":    "⚠ Warning Triangle",
				"Layer":        "Top (Float below Waybar)",
			},
		},
	}

	wd, _ := os.Getwd()
	repoRoot := filepath.Clean(filepath.Join(wd, "..", ".."))
	reportsDir := filepath.Join(repoRoot, "test", "reports")
	screenshotsDir := filepath.Join(reportsDir, "screenshots")
	if err := os.MkdirAll(screenshotsDir, 0o755); err != nil {
		t.Fatalf("mkdir screenshots dir: %v", err)
	}

	var captures []StateCapture

	for _, s := range states {
		t.Logf("Capturing state %d: %s (%s)", s.Index, s.Title, s.ID)

		if err := ov.Show(s.Visual); err != nil {
			t.Fatalf("Show(%v): %v", s.Visual, err)
		}
		if s.Visual == overlay.Recording {
			// Feed one full waveform window (46 samples) of speech-like
			// levels — a spoken phrase with inter-word gaps, scaled to the
			// state's AudioLevel — so the screenshot shows a filled,
			// scrolling waveform (newest sample at the live edge), not a
			// partial ramp. 46 ticks at 20ms ≈ 0.92s of feed.
			for _, p := range speechEnvelope {
				if err := ov.SetLevel(s.AudioLevel * p); err != nil {
					t.Fatalf("SetLevel(%f): %v", s.AudioLevel*p, err)
				}
				time.Sleep(20 * time.Millisecond)
			}
		}

		// Wait for GTK and Sway surface composition
		time.Sleep(350 * time.Millisecond)

		fullBytes := h.Grim()
		if len(fullBytes) == 0 {
			t.Fatalf("Grim returned empty screenshot for state %s", s.ID)
		}

		fullImg, err := png.Decode(bytes.NewReader(fullBytes))
		if err != nil {
			t.Fatalf("decode full png: %v", err)
		}

		croppedImg := cropTop(fullImg, cropHeight)
		cropBytes, err := encodePNG(croppedImg)
		if err != nil {
			t.Fatalf("encode cropped png: %v", err)
		}

		fullFileName := fmt.Sprintf("%02d_%s_full.png", s.Index, s.ID)
		cropFileName := fmt.Sprintf("%02d_%s_crop.png", s.Index, s.ID)
		fullPath := filepath.Join(screenshotsDir, fullFileName)
		cropPath := filepath.Join(screenshotsDir, cropFileName)

		if err := os.WriteFile(fullPath, fullBytes, 0o644); err != nil {
			t.Fatalf("write full screenshot: %v", err)
		}
		if err := os.WriteFile(cropPath, cropBytes, 0o644); err != nil {
			t.Fatalf("write cropped screenshot: %v", err)
		}

		captures = append(captures, StateCapture{
			State:        s,
			FullB64:      base64.StdEncoding.EncodeToString(fullBytes),
			CropB64:      base64.StdEncoding.EncodeToString(cropBytes),
			FullFileName: fullFileName,
			CropFileName: cropFileName,
			FullRelPath:  filepath.Join("screenshots", fullFileName),
			CropRelPath:  filepath.Join("screenshots", cropFileName),
			Width:        fullImg.Bounds().Dx(),
			Height:       fullImg.Bounds().Dy(),
			CropHeight:   cropHeight,
			SizeBytes:    len(fullBytes),
			SizeKB:       fmt.Sprintf("%.1f KB", float64(len(fullBytes))/1024.0),
		})
	}

	reportData := ReportData{
		GeneratedAt:  generatedAt(),
		TotalStates:  len(captures),
		Compositor:   "Headless Sway (wlroots / pixman)",
		DisplayRes:   fmt.Sprintf("%dx%d", dispWidth, dispHeight),
		WaybarHeight: waybarHeight,
		TopMargin:    topMargin,
		Captures:     captures,
	}

	reportPath := filepath.Join(reportsDir, "ui-storybook.html")
	if err := generateHTMLReport(reportPath, reportData); err != nil {
		t.Fatalf("generateHTMLReport: %v", err)
	}

	t.Logf("Successfully generated UI Storybook report at: %s", reportPath)
}

func generateHTMLReport(outPath string, data ReportData) error {
	tmpl, err := template.New("storybook").Parse(storybookHTMLTemplate)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}
	return os.WriteFile(outPath, buf.Bytes(), 0o644)
}

const storybookHTMLTemplate = `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>mavor UI Storybook · Headless Wayland Screenshots</title>
  <style>
    :root {
      --bg: #0b0f19;
      --bg-surface: #111827;
      --bg-card: #1f2937;
      --bg-card-hover: #263345;
      --border: #374151;
      --border-subtle: #1f2937;
      --text: #f9fafb;
      --text-muted: #9ca3af;
      --text-dim: #6b7280;
      --accent: #3b82f6;
      --accent-glow: rgba(59, 130, 246, 0.2);
      --red: #ef4444;
      --amber: #f59e0b;
      --crimson: #dc2626;
      --green: #10b981;
      --radius: 12px;
      --radius-sm: 6px;
      --shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.5), 0 8px 10px -6px rgba(0, 0, 0, 0.4);
      --shadow-sm: 0 4px 6px -1px rgba(0, 0, 0, 0.3);
      --font: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Inter", Helvetica, Arial, sans-serif;
      --font-mono: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
    }

    [data-theme="light"] {
      --bg: #f3f4f6;
      --bg-surface: #ffffff;
      --bg-card: #ffffff;
      --bg-card-hover: #f9fafb;
      --border: #e5e7eb;
      --border-subtle: #f3f4f6;
      --text: #111827;
      --text-muted: #4b5563;
      --text-dim: #9ca3af;
      --accent: #2563eb;
      --accent-glow: rgba(37, 99, 235, 0.15);
      --shadow: 0 10px 25px -5px rgba(0, 0, 0, 0.08), 0 8px 10px -6px rgba(0, 0, 0, 0.04);
      --shadow-sm: 0 4px 6px -1px rgba(0, 0, 0, 0.05);
    }

    * {
      box-sizing: border-box;
      margin: 0;
      padding: 0;
    }

    body {
      font-family: var(--font);
      background-color: var(--bg);
      color: var(--text);
      line-height: 1.5;
      -webkit-font-smoothing: antialiased;
      transition: background-color 0.2s ease, color 0.2s ease;
      padding-bottom: 80px;
    }

    .container {
      max-width: 1400px;
      margin: 0 auto;
      padding: 0 24px;
    }

    header {
      background: var(--bg-surface);
      border-bottom: 1px solid var(--border);
      padding: 24px 0;
      position: sticky;
      top: 0;
      z-index: 100;
      backdrop-filter: blur(12px);
    }

    .header-content {
      display: flex;
      justify-content: space-between;
      align-items: center;
      flex-wrap: wrap;
      gap: 16px;
    }

    .logo-group {
      display: flex;
      align-items: center;
      gap: 14px;
    }

    .logo-badge {
      background: linear-gradient(135deg, #3b82f6, #1d4ed8);
      color: white;
      font-weight: 800;
      font-size: 16px;
      padding: 6px 12px;
      border-radius: var(--radius-sm);
      letter-spacing: 0.05em;
    }

    .header-title h1 {
      font-size: 22px;
      font-weight: 700;
      letter-spacing: -0.02em;
      display: flex;
      align-items: center;
      gap: 10px;
    }

    .header-title p {
      font-size: 13px;
      color: var(--text-muted);
    }

    .header-controls {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .btn {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 8px 14px;
      font-size: 13px;
      font-weight: 600;
      border-radius: var(--radius-sm);
      border: 1px solid var(--border);
      background: var(--bg-card);
      color: var(--text);
      cursor: pointer;
      transition: all 0.15s ease;
      text-decoration: none;
    }

    .btn:hover {
      background: var(--bg-card-hover);
      border-color: var(--text-muted);
    }

    .btn.active {
      background: var(--accent);
      color: white;
      border-color: var(--accent);
    }

    .theme-toggle {
      padding: 8px 12px;
    }

    /* Summary Stats Grid */
    .stats-bar {
      margin: 32px 0 24px 0;
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
      gap: 16px;
    }

    .stat-card {
      background: var(--bg-surface);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      padding: 16px 20px;
      box-shadow: var(--shadow-sm);
    }

    .stat-label {
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--text-muted);
      font-weight: 600;
    }

    .stat-value {
      font-size: 20px;
      font-weight: 700;
      margin-top: 4px;
      color: var(--text);
      font-family: var(--font-mono);
    }

    .stat-sub {
      font-size: 12px;
      color: var(--text-dim);
      margin-top: 2px;
    }

    /* Toolbar Filter & View Switcher */
    .toolbar {
      background: var(--bg-surface);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      padding: 14px 20px;
      margin-bottom: 32px;
      display: flex;
      justify-content: space-between;
      align-items: center;
      flex-wrap: wrap;
      gap: 16px;
    }

    .filter-group, .view-group {
      display: flex;
      align-items: center;
      gap: 8px;
      flex-wrap: wrap;
    }

    .filter-label {
      font-size: 13px;
      font-weight: 600;
      color: var(--text-muted);
      margin-right: 4px;
    }

    /* State Cards Layout */
    .state-feed {
      display: flex;
      flex-direction: column;
      gap: 36px;
    }

    .state-card {
      background: var(--bg-surface);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      overflow: hidden;
      box-shadow: var(--shadow);
      transition: transform 0.15s ease, border-color 0.15s ease;
    }

    .state-card:hover {
      border-color: var(--border-subtle);
    }

    .card-header {
      padding: 18px 24px;
      border-bottom: 1px solid var(--border);
      background: var(--bg-card);
      display: flex;
      justify-content: space-between;
      align-items: center;
      flex-wrap: wrap;
      gap: 12px;
    }

    .card-title-group {
      display: flex;
      align-items: center;
      gap: 12px;
    }

    .state-index {
      font-family: var(--font-mono);
      font-size: 13px;
      font-weight: 700;
      color: var(--text-dim);
      background: var(--bg-surface);
      padding: 2px 8px;
      border-radius: var(--radius-sm);
      border: 1px solid var(--border);
    }

    .card-title {
      font-size: 18px;
      font-weight: 700;
      letter-spacing: -0.01em;
    }

    .badge {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 11px;
      font-weight: 800;
      letter-spacing: 0.08em;
      text-transform: uppercase;
      padding: 4px 10px;
      border-radius: 9999px;
    }

    .badge-recording {
      background: rgba(239, 68, 68, 0.2);
      color: #f87171;
      border: 1px solid rgba(239, 68, 68, 0.4);
    }
    .badge-recording::before {
      content: "";
      display: inline-block;
      width: 6px;
      height: 6px;
      background: #ef4444;
      border-radius: 50%;
      animation: pulse 1s infinite alternate;
    }

    .badge-transcribing {
      background: rgba(245, 158, 11, 0.2);
      color: #fbbf24;
      border: 1px solid rgba(245, 158, 11, 0.4);
    }

    .badge-error {
      background: rgba(220, 38, 38, 0.2);
      color: #fca5a5;
      border: 1px solid rgba(220, 38, 38, 0.4);
    }

    .badge-hidden {
      background: rgba(107, 114, 128, 0.2);
      color: #9ca3af;
      border: 1px solid rgba(107, 114, 128, 0.3);
    }

    @keyframes pulse {
      from { opacity: 0.4; }
      to { opacity: 1; }
    }

    .card-body {
      padding: 24px;
    }

    .card-desc {
      font-size: 14px;
      color: var(--text-muted);
      margin-bottom: 20px;
      max-width: 900px;
      line-height: 1.6;
    }

    .card-meta-grid {
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(220px, 1fr));
      gap: 12px;
      margin-bottom: 24px;
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      padding: 14px 18px;
    }

    .meta-item {
      display: flex;
      flex-direction: column;
    }

    .meta-key {
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--text-dim);
      font-weight: 600;
    }

    .meta-val {
      font-size: 13px;
      font-weight: 600;
      color: var(--text);
      font-family: var(--font-mono);
      margin-top: 2px;
    }

    /* Screenshots Area */
    .previews-container {
      display: flex;
      flex-direction: column;
      gap: 20px;
    }

    .preview-section {
      background: var(--bg);
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      overflow: hidden;
    }

    .preview-header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 10px 16px;
      background: var(--bg-card);
      border-bottom: 1px solid var(--border);
      font-size: 12px;
      font-weight: 600;
      color: var(--text-muted);
    }

    .preview-tag {
      font-family: var(--font-mono);
      font-size: 11px;
      background: var(--bg);
      padding: 2px 6px;
      border-radius: 4px;
      border: 1px solid var(--border);
      color: var(--text-dim);
    }

    .image-viewport {
      position: relative;
      background: #000000;
      display: flex;
      justify-content: center;
      align-items: flex-start;
      overflow: hidden;
      cursor: zoom-in;
    }

    .image-viewport img {
      width: 100%;
      height: auto;
      display: block;
      transition: transform 0.2s ease;
    }

    .image-viewport.zoom-view img {
      max-height: 280px;
      object-fit: contain;
      object-position: top center;
    }

    .image-viewport.desktop-view img {
      max-height: 480px;
      object-fit: contain;
    }

    .image-viewport:hover img {
      opacity: 0.95;
    }

    /* View Modes */
    body.mode-crop-only .desktop-preview-section {
      display: none;
    }

    body.mode-desktop-only .crop-preview-section {
      display: none;
    }

    body.mode-split .previews-container {
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 16px;
    }

    @media (max-width: 900px) {
      body.mode-split .previews-container {
        grid-template-columns: 1fr;
      }
    }

    /* Lightbox Modal */
    .lightbox {
      position: fixed;
      inset: 0;
      background: rgba(0, 0, 0, 0.9);
      z-index: 1000;
      display: none;
      justify-content: center;
      align-items: center;
      padding: 40px;
      backdrop-filter: blur(8px);
    }

    .lightbox.active {
      display: flex;
    }

    .lightbox-content {
      max-width: 95vw;
      max-height: 90vh;
      position: relative;
      box-shadow: 0 25px 50px -12px rgba(0, 0, 0, 0.75);
      border-radius: var(--radius-sm);
      overflow: hidden;
      border: 1px solid #374151;
    }

    .lightbox-content img {
      max-width: 100%;
      max-height: 85vh;
      display: block;
    }

    .lightbox-close {
      position: absolute;
      top: 16px;
      right: 20px;
      font-size: 28px;
      color: white;
      cursor: pointer;
      background: rgba(0, 0, 0, 0.6);
      width: 40px;
      height: 40px;
      display: flex;
      align-items: center;
      justify-content: center;
      border-radius: 50%;
      border: 1px solid rgba(255, 255, 255, 0.2);
    }

    .lightbox-caption {
      background: #111827;
      color: #9ca3af;
      padding: 10px 16px;
      font-size: 13px;
      font-family: var(--font-mono);
      text-align: center;
      border-top: 1px solid #374151;
    }

    footer {
      margin-top: 60px;
      text-align: center;
      color: var(--text-dim);
      font-size: 13px;
      border-top: 1px solid var(--border);
      padding: 24px 0;
    }
  </style>
</head>
<body>

  <header>
    <div class="container header-content">
      <div class="logo-group">
        <span class="logo-badge">mavor</span>
        <div class="header-title">
          <h1>UI Storybook Report</h1>
          <p>Headless Wayland GTK4 Layer-Shell Visual Inspection</p>
        </div>
      </div>
      <div class="header-controls">
        <button class="btn theme-toggle" id="themeToggle" title="Toggle Light/Dark Theme">
          <span id="themeIcon">☀️</span> Light / Dark
        </button>
        <a href="#state-1" class="btn active">States ({{.TotalStates}})</a>
      </div>
    </div>
  </header>

  <main class="container">
    <section class="stats-bar">
      <div class="stat-card">
        <div class="stat-label">Compositor</div>
        <div class="stat-value">Sway 1.10</div>
        <div class="stat-sub">wlroots headless / pixman</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Virtual Output</div>
        <div class="stat-value">{{.DisplayRes}}</div>
        <div class="stat-sub">60Hz · HEADLESS-1</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Shell Layout</div>
        <div class="stat-value">Top Float</div>
        <div class="stat-sub">Waybar {{.WaybarHeight}}px + Margin {{.TopMargin}}px</div>
      </div>
      <div class="stat-card">
        <div class="stat-label">Report Built</div>
        <div class="stat-value" style="font-size: 15px;">{{.GeneratedAt}}</div>
        <div class="stat-sub">Automated Headless Test</div>
      </div>
    </section>

    <div class="toolbar">
      <div class="filter-group">
        <span class="filter-label">Filter:</span>
        <button class="btn active" data-filter="all">All ({{.TotalStates}})</button>
        <button class="btn" data-filter="recording">Recording Levels</button>
        <button class="btn" data-filter="transcribing">Transcribing</button>
        <button class="btn" data-filter="error">Error</button>
        <button class="btn" data-filter="hidden">Hidden</button>
      </div>
      <div class="view-group">
        <span class="filter-label">View Mode:</span>
        <button class="btn active" data-view="both">Stacked (Zoom + Desktop)</button>
        <button class="btn" data-view="split">Side-by-Side</button>
        <button class="btn" data-view="crop-only">Zoom Only</button>
        <button class="btn" data-view="desktop-only">Full Desktop</button>
      </div>
    </div>

    <div class="state-feed">
      {{range .Captures}}
      <article class="state-card" id="state-{{.State.Index}}" data-category="{{if eq .State.Visual 1}}recording{{else if eq .State.Visual 2}}transcribing{{else if eq .State.Visual 3}}error{{else}}hidden{{end}}">
        <div class="card-header">
          <div class="card-title-group">
            <span class="state-index">#0{{.State.Index}}</span>
            <h2 class="card-title">{{.State.Title}}</h2>
            <span class="badge {{.State.BadgeClass}}">{{.State.Badge}}</span>
          </div>
          <div class="card-actions">
            <a class="btn" href="{{.FullRelPath}}" download title="Download raw lossless PNG">💾 Save PNG ({{.SizeKB}})</a>
          </div>
        </div>

        <div class="card-body">
          <p class="card-desc">{{.State.Description}}</p>

          <div class="card-meta-grid">
            {{range $key, $val := .State.Specs}}
            <div class="meta-item">
              <span class="meta-key">{{$key}}</span>
              <span class="meta-val">{{$val}}</span>
            </div>
            {{end}}
            <div class="meta-item">
              <span class="meta-key">Resolution / Size</span>
              <span class="meta-val">{{$.DisplayRes}} ({{.SizeKB}})</span>
            </div>
          </div>

          <div class="previews-container">
            <!-- Focused Overlay Zoom -->
            <div class="preview-section crop-preview-section">
              <div class="preview-header">
                <span>Focused Overlay View (Waybar + Top Margin + Floating Pill)</span>
                <span class="preview-tag">{{.Width}} × {{.CropHeight}}px Crop</span>
              </div>
              <div class="image-viewport zoom-view" onclick="openLightbox('data:image/png;base64,{{.CropB64}}', '{{.State.Title}} · Zoomed Crop ({{.Width}}x{{.CropHeight}}px)')">
                <img src="data:image/png;base64,{{.CropB64}}" alt="{{.State.Title}} (Zoomed Crop)" loading="lazy">
              </div>
            </div>

            <!-- Full Desktop View -->
            <div class="preview-section desktop-preview-section">
              <div class="preview-header">
                <span>Full Headless Sway Desktop Frame</span>
                <span class="preview-tag">{{.Width}} × {{.Height}}px Canvas</span>
              </div>
              <div class="image-viewport desktop-view" onclick="openLightbox('data:image/png;base64,{{.FullB64}}', '{{.State.Title}} · Full Desktop Screenshot ({{.Width}}x{{.Height}}px)')">
                <img src="data:image/png;base64,{{.FullB64}}" alt="{{.State.Title}} (Full Desktop)" loading="lazy">
              </div>
            </div>
          </div>
        </div>
      </article>
      {{end}}
    </div>
  </main>

  <!-- Lightbox Modal -->
  <div class="lightbox" id="lightbox" onclick="closeLightbox(event)">
    <div class="lightbox-close" onclick="closeLightbox(event)">&times;</div>
    <div class="lightbox-content" onclick="event.stopPropagation()">
      <img id="lightboxImg" src="" alt="Full view">
      <div class="lightbox-caption" id="lightboxCaption"></div>
    </div>
  </div>

  <footer>
    <div class="container">
      <p>mavor · Automated UI Storybook Report · Real GTK4 Layer-Shell Screenshots via Grim on Headless Sway</p>
    </div>
  </footer>

  <script>
    // Theme Toggle
    const themeToggle = document.getElementById('themeToggle');
    const themeIcon = document.getElementById('themeIcon');
    let currentTheme = 'dark';

    themeToggle.addEventListener('click', () => {
      currentTheme = currentTheme === 'dark' ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', currentTheme);
      themeIcon.textContent = currentTheme === 'dark' ? '☀️' : '🌙';
    });

    // Category Filter
    const filterButtons = document.querySelectorAll('.filter-group .btn');
    const cards = document.querySelectorAll('.state-card');

    filterButtons.forEach(btn => {
      btn.addEventListener('click', () => {
        filterButtons.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');

        const filter = btn.dataset.filter;
        cards.forEach(card => {
          if (filter === 'all' || card.dataset.category === filter) {
            card.style.display = 'block';
          } else {
            card.style.display = 'none';
          }
        });
      });
    });

    // View Mode Switcher
    const viewButtons = document.querySelectorAll('.view-group .btn');
    viewButtons.forEach(btn => {
      btn.addEventListener('click', () => {
        viewButtons.forEach(b => b.classList.remove('active'));
        btn.classList.add('active');

        document.body.classList.remove('mode-crop-only', 'mode-desktop-only', 'mode-split');
        const view = btn.dataset.view;
        if (view === 'crop-only') {
          document.body.classList.add('mode-crop-only');
        } else if (view === 'desktop-only') {
          document.body.classList.add('mode-desktop-only');
        } else if (view === 'split') {
          document.body.classList.add('mode-split');
        }
      });
    });

    // Lightbox
    const lightbox = document.getElementById('lightbox');
    const lightboxImg = document.getElementById('lightboxImg');
    const lightboxCaption = document.getElementById('lightboxCaption');

    function openLightbox(src, caption) {
      lightboxImg.src = src;
      lightboxCaption.textContent = caption;
      lightbox.classList.add('active');
    }

    function closeLightbox(e) {
      lightbox.classList.remove('active');
      lightboxImg.src = '';
    }

    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && lightbox.classList.contains('active')) {
        closeLightbox();
      }
    });
  </script>
</body>
</html>
`
