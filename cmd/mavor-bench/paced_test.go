package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/internal/audio"
	"github.com/mschulkind-oss/mavor/internal/models"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// pacePCM delivers samples only once their recording interval has elapsed.
// Feed is nonblocking production queue admission, never the decoder itself.
// Absolute deadlines avoid adding inference time to the simulated recording.
func pacePCM(pcm []byte, start time.Time, now func() time.Time, wait func(time.Duration), feed func([]byte) error) (time.Time, time.Duration, string) {
	var late time.Duration
	var firstError string
	for pos := 0; pos < len(pcm); {
		end := min(pos+audio.FrameSamples*2, len(pcm))
		deadline := start.Add(time.Duration(end/2) * time.Second / 16000)
		if delay := deadline.Sub(now()); delay > 0 {
			wait(delay)
		}
		late = max(late, now().Sub(deadline))
		if err := feed(pcm[pos:end]); err != nil && firstError == "" {
			firstError = err.Error()
		}
		pos = end
	}
	return now(), late, firstError
}

func TestPacedProducerDeadlinesAndTail(t *testing.T) {
	start := time.Unix(0, 0)
	clock := start
	pcm := make([]byte, 2*(2*audio.FrameSamples+7))
	for i := range pcm {
		pcm[i] = byte(i)
	}
	var seen []byte
	var calls int
	release, late, _ := pacePCM(pcm, start, func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) }, func(b []byte) error {
		calls++
		seen = append(seen, b...)
		// Admission overhead must not shift subsequent recording deadlines.
		if calls == 1 {
			clock = clock.Add(2 * time.Millisecond)
		}
		return nil
	})
	want := time.Duration(len(pcm)/2) * time.Second / 16000
	if !bytes.Equal(seen, pcm) || calls != 3 || release.Sub(start) != want || late != 0 {
		t.Fatalf("coverage/cadence: bytes=%d calls=%d duration=%v late=%v", len(seen), calls, release.Sub(start), late)
	}
}

func TestConfiguredModePacedEligibility(t *testing.T) {
	for model, mode := range map[string]models.FinalMode{"nemotron-streaming-en-560ms": models.FinalStreaming, "parakeet-tdt-0.6b-v2": models.FinalSegments} {
		if err := models.ValidateFinalSelection(model, mode); err != nil {
			t.Fatal(err)
		}
	}
}

type pacedObservation struct {
	Model, Fixture, WAVSHA256, ReferenceSHA256               string
	RequestedMode                                            models.FinalMode
	LoadMS, SessionStartMS, ReleaseToFinalMS, ProducerLateMS float64
	RecordingStart, Release, Complete                        time.Time
	Result                                                   speech.FinalResult
	FeedError, Error                                         string
	Transcript                                               string
	WER, RawTokenWER                                         float64
	PhraseCounts                                             map[string][2]int
	LoadBefore, LoadAfter                                    string
	CPUInfo                                                  string
	Threads, CPUs, GOMAXPROCS                                int
	PeakRSSKB                                                int64
}

func fileHash(path string) string {
	b, _ := os.ReadFile(path)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func pacedLoadAverage() string {
	b, _ := os.ReadFile("/proc/loadavg")
	return strings.TrimSpace(string(b))
}

// TestPacedFinalModels is deliberately opt-in, no download, microphone, output,
// clipboard, OSC, or services. Run each model in a fresh process for meaningful
// process peak RSS. Output is JSONL, outside generated historical reports.
func TestPacedFinalModels(t *testing.T) {
	model := os.Getenv("MAVOR_PACED_MODEL")
	if model == "" {
		t.Skip("set MAVOR_PACED_MODEL, MAVOR_PACED_MODELS and MAVOR_PACED_OUTPUT explicitly")
	}
	root, out := os.Getenv("MAVOR_PACED_MODELS"), os.Getenv("MAVOR_PACED_OUTPUT")
	if root == "" || out == "" {
		t.Fatal("explicit model root and evidence output required")
	}
	mode := models.FinalStreaming
	if model == "parakeet-tdt-0.6b-v2" {
		mode = models.FinalSegments
	} else if model != "nemotron-streaming-en-560ms" {
		t.Fatal("bounded prototype candidates only")
	}
	cfg := (sherpaRunner{modelDir: root, threads: 4}).config(model)
	cfg.Advanced.FinalMode = string(mode)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	loadStart := time.Now()
	tr, err := speech.Factory(cfg, quietLogger())
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := tr.(interface {
		Start(context.Context) error
		Close() error
	})
	defer func() {
		if err := lifecycle.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := lifecycle.Start(ctx); err != nil {
		t.Fatal(err)
	}
	loadMS := float64(time.Since(loadStart)) / float64(time.Millisecond)
	output, err := os.OpenFile(out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	enc := json.NewEncoder(output)
	cpu, _ := os.ReadFile("/proc/cpuinfo")
	for _, fixture := range []string{"real_speech.wav", "llm_prompt_68s.wav"} {
		path := filepath.Join("../../test/fixtures", fixture)
		samples, err := audio.ReadWAVSamples(path)
		if err != nil {
			t.Fatal(err)
		}
		pcm := make([]byte, len(samples)*2)
		for i, v := range samples {
			binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
		}
		refBytes, err := os.ReadFile(path + ".txt")
		if err != nil {
			t.Fatal(err)
		}
		ref := strings.TrimSpace(string(refBytes))
		for _, selected := range []models.FinalMode{models.FinalAfterStop, mode} {
			o := pacedObservation{Model: model, Fixture: fixture, RequestedMode: selected, LoadMS: loadMS, Threads: 4, CPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), CPUInfo: string(cpu), LoadBefore: pacedLoadAverage(), WAVSHA256: fileHash(path), ReferenceSHA256: fileHash(path + ".txt")}
			sessionStart := time.Now()
			s, e := speech.NewFinalSession(ctx, tr, speech.FinalSessionOptions{Mode: selected, Logger: quietLogger()})
			if e != nil {
				t.Fatal(e)
			}
			o.SessionStartMS = float64(time.Since(sessionStart)) / float64(time.Millisecond)
			o.RecordingStart = time.Now()
			release, late, feedErr := pacePCM(pcm, o.RecordingStart, time.Now, time.Sleep, s.Feed)
			o.Release = release
			o.ProducerLateMS = float64(late) / float64(time.Millisecond)
			o.FeedError = feedErr
			o.Result, e = s.Finish(ctx, path)
			o.Complete = time.Now()
			if e != nil {
				o.Error = e.Error()
			}
			o.ReleaseToFinalMS = float64(o.Complete.Sub(o.Release)) / float64(time.Millisecond)
			o.Transcript = o.Result.Text
			o.WER = wordErrorRate(ref, o.Transcript)
			// Literal token scoring keeps punctuation/case and boundary differences visible.
			rawRef, rawHyp := strings.Fields(ref), strings.Fields(o.Transcript)
			o.RawTokenWER = float64(editDistance(rawRef, rawHyp)) / float64(len(rawRef))
			o.PhraseCounts = map[string][2]int{}
			for _, phrase := range []string{"Lux", "hops", "records", "internal/speech", "internal/output", "repetition loops", "ripgrep", "Wayland compositor", "benchmark sample", "what broke"} {
				o.PhraseCounts[phrase] = [2]int{strings.Count(ref, phrase), strings.Count(o.Transcript, phrase)}
			}
			o.LoadAfter = pacedLoadAverage()
			var usage syscall.Rusage
			if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) == nil {
				o.PeakRSSKB = usage.Maxrss
			}
			if err := enc.Encode(o); err != nil {
				t.Fatal(err)
			}
			t.Logf("%s %s %s release %.1fms WER %.3f actual %s replay %q", model, fixture, selected, o.ReleaseToFinalMS, o.WER, o.Result.ActualMode, o.Result.ReplayReason)
			if e != nil {
				t.Error(fmt.Errorf("final recognition: %w", e))
			}
			if e == nil && o.Result.Stats.CapturedSamples != int64(len(samples)) {
				t.Errorf("captured coverage: %d != %d", o.Result.Stats.CapturedSamples, len(samples))
			}
			if selected != models.FinalAfterStop && e == nil && o.Result.ActualMode == selected && o.Result.Stats.LiveSamples != int64(len(samples)) {
				t.Errorf("successful live coverage: %d != %d", o.Result.Stats.LiveSamples, len(samples))
			}
		}
	}
}

func TestPacedProducerLateAdmissionKeepsCoverage(t *testing.T) {
	start := time.Unix(0, 0)
	clock := start
	pcm := make([]byte, 1922)
	calls := 0
	seen := 0
	release, late, feedErr := pacePCM(pcm, start, func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) }, func(b []byte) error {
		calls++
		seen += len(b)
		if calls == 1 {
			clock = clock.Add(40 * time.Millisecond)
			return fmt.Errorf("admission failed")
		}
		return nil
	})
	if calls != 3 || seen != len(pcm) || late != 10*time.Millisecond || release.Sub(start) != 70*time.Millisecond || feedErr != "admission failed" {
		t.Fatalf("calls=%d seen=%d late=%v release=%v error=%q", calls, seen, late, release.Sub(start), feedErr)
	}
}
