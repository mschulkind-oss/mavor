package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// benchWhisper runs one whisper model on one device, o.runs times, and folds
// the runs into a single row. The transcript kept is the last successful one:
// whisper is deterministic at these settings, so they agree, and keeping one
// makes the accuracy columns reproducible from the JSON alone.
func benchWhisper(ctx context.Context, w whisperRunner, m catalogModel, modelDir string, o options, reference string, audioSec float64) runResult {
	res := runResult{
		Model:   m.Name,
		Family:  m.Family,
		Backend: backend{Engine: "whisper-cli", Device: w.device, Build: w.build, Mode: "batch", Binary: w.binary},
	}
	modelPath := filepath.Join(modelDir, "ggml-"+m.Name+".bin")
	if _, err := os.Stat(modelPath); err != nil {
		res.Failed, res.Error = true, fmt.Sprintf("model file not found at %s", modelPath)
		return res
	}

	var times []float64
	var text string
	var peak int64
	for i := 0; i < o.runs; i++ {
		runCtx, cancel := context.WithTimeout(ctx, o.timeout)
		out, elapsed, rss, err := w.run(runCtx, modelPath, o.audio)
		cancel()
		if err != nil {
			res.Failed, res.Error = true, err.Error()
			return res
		}
		times = append(times, float64(elapsed)/float64(time.Millisecond))
		text = out
		peak = max(peak, rss)
	}

	res.Runs = len(times)
	res.TotalMS = median(times)
	res.PeakRSSKB = peak
	res.RTF = res.TotalMS / 1000 / audioSec
	scoreAccuracy(&res, reference, text)
	return res
}

// benchSherpaBatch runs one sherpa model over the whole file at once.
func benchSherpaBatch(ctx context.Context, s sherpaRunner, m catalogModel, o options, reference string, audioSec float64) runResult {
	res := runResult{
		Model:   m.Name,
		Family:  m.Family,
		Backend: backend{Engine: "sherpa", Device: "cpu", Mode: "batch"},
	}

	var totals, loads []float64
	var text string
	var peak int64
	for i := 0; i < o.runs; i++ {
		runCtx, cancel := context.WithTimeout(ctx, o.timeout)
		out, load, infer, rss, err := s.runBatch(runCtx, m.Name, o.audio)
		cancel()
		if err != nil {
			res.Failed, res.Error = true, err.Error()
			return res
		}
		totals = append(totals, float64(load+infer)/float64(time.Millisecond))
		loads = append(loads, float64(load)/float64(time.Millisecond))
		text = out
		peak = max(peak, rss)
	}

	res.Runs = len(totals)
	res.TotalMS = median(totals)
	res.LoadMS = median(loads)
	res.PeakRSSKB = peak
	res.RTF = res.TotalMS / 1000 / audioSec
	scoreAccuracy(&res, reference, text)
	return res
}

// benchSherpaStreaming feeds the same audio in chunks, as the daemon does
// while you are still speaking, and records time to first partial text.
func benchSherpaStreaming(ctx context.Context, s sherpaRunner, m catalogModel, o options, reference string, audioSec float64) runResult {
	res := runResult{
		Model:   m.Name,
		Family:  m.Family,
		Backend: backend{Engine: "sherpa", Device: "cpu", Mode: "streaming"},
	}

	var totals, firsts []float64
	var text string
	var peak int64
	for i := 0; i < o.runs; i++ {
		runCtx, cancel := context.WithTimeout(ctx, o.timeout)
		out, firstToken, total, rss, err := s.runStreaming(runCtx, m.Name, o.audio)
		cancel()
		if err != nil {
			res.Failed, res.Error = true, err.Error()
			return res
		}
		totals = append(totals, float64(total)/float64(time.Millisecond))
		if firstToken > 0 {
			firsts = append(firsts, float64(firstToken)/float64(time.Millisecond))
		}
		text = out
		peak = max(peak, rss)
	}

	res.Runs = len(totals)
	res.TotalMS = median(totals)
	res.FirstTokenMS = median(firsts)
	res.PeakRSSKB = peak
	res.RTF = res.TotalMS / 1000 / audioSec
	scoreAccuracy(&res, reference, text)
	return res
}

// scoreAccuracy fills the accuracy columns from one transcript. It is
// separate from the timing loop so that every backend is scored by identical
// code — a per-engine scoring path is how two engines end up with numbers
// that are not comparable.
func scoreAccuracy(res *runResult, reference, text string) {
	res.Transcript = text
	res.WER = wordErrorRate(reference, text)
	res.CER = characterErrorRate(reference, text)
	res.PunctDens = punctuationDensity(text)
	res.CapF1 = capitalizationF1(reference, text)
}

// wavDurationSeconds reads the RIFF header for the audio length. Every
// real-time factor in the report divides by this, so it is read from the file
// rather than configured: a mismatched duration would scale every number in
// the report by a constant and look entirely plausible.
func wavDurationSeconds(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	header := make([]byte, 12)
	if _, err := f.Read(header); err != nil {
		return 0, err
	}
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return 0, fmt.Errorf("%s is not a RIFF/WAVE file", path)
	}

	var byteRate uint32
	var dataBytes uint32
	for {
		chunk := make([]byte, 8)
		if _, err := f.Read(chunk); err != nil {
			break
		}
		id := string(chunk[0:4])
		size := binary.LittleEndian.Uint32(chunk[4:8])
		switch id {
		case "fmt ":
			body := make([]byte, size)
			if _, err := f.Read(body); err != nil {
				return 0, err
			}
			if len(body) >= 12 {
				byteRate = binary.LittleEndian.Uint32(body[8:12])
			}
		case "data":
			dataBytes = size
			if byteRate == 0 {
				return 0, fmt.Errorf("%s has a data chunk before its fmt chunk", path)
			}
			return float64(dataBytes) / float64(byteRate), nil
		default:
			if _, err := f.Seek(int64(size), 1); err != nil {
				return 0, err
			}
		}
	}
	return 0, fmt.Errorf("%s has no data chunk", path)
}
