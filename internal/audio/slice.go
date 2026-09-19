package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"time"
)

// AudioDuration returns the duration of a 16-bit mono 16kHz RIFF/WAVE file
// by inspecting the WAV header and data chunk size without loading samples into memory.
func AudioDuration(wavPath string) (time.Duration, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return 0, fmt.Errorf("audio: open %s: %w", wavPath, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return 0, err
	}
	dataAt, err := WAVDataOffset(f)
	if err != nil {
		return 0, err
	}
	dataBytes := stat.Size() - dataAt
	if dataBytes <= 0 {
		return 0, nil
	}
	// 16kHz * 2 bytes per sample = 32000 bytes per second
	seconds := float64(dataBytes) / float64(DefaultSampleRate*2)
	return time.Duration(seconds * float64(time.Second)), nil
}

// SliceWAV copies samples from startSample to endSample of srcPath into dstPath,
// adding a standard 44-byte WAV header (16kHz, mono, s16le).
func SliceWAV(srcPath, dstPath string, startSample, endSample int64) error {
	if endSample <= startSample {
		return fmt.Errorf("audio: invalid slice range: start=%d end=%d", startSample, endSample)
	}
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("audio: open %s: %w", srcPath, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}
	dataAt, err := WAVDataOffset(f)
	if err != nil {
		return err
	}
	totalSamples := (stat.Size() - dataAt) / 2
	if startSample < 0 {
		startSample = 0
	}
	if endSample > totalSamples {
		endSample = totalSamples
	}
	if endSample <= startSample {
		return fmt.Errorf("audio: slice range %d..%d outside sample count %d", startSample, endSample, totalSamples)
	}

	sampleCount := endSample - startSample
	dataSize := uint32(sampleCount * 2)
	fileSize := 36 + dataSize

	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("audio: create %s: %w", dstPath, err)
	}
	defer out.Close()

	// Write 44-byte standard RIFF/WAVE header
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], fileSize)
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1) // PCM format
	binary.LittleEndian.PutUint16(header[22:24], 1) // Mono
	binary.LittleEndian.PutUint32(header[24:28], uint32(DefaultSampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(DefaultSampleRate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], dataSize)

	if _, err := out.Write(header); err != nil {
		return fmt.Errorf("audio: write header: %w", err)
	}

	byteOffset := dataAt + startSample*2
	if _, err := f.Seek(byteOffset, io.SeekStart); err != nil {
		return fmt.Errorf("audio: seek %s: %w", srcPath, err)
	}

	bytesToCopy := int64(sampleCount * 2)
	if _, err := io.CopyN(out, f, bytesToCopy); err != nil {
		return fmt.Errorf("audio: copy samples: %w", err)
	}

	return nil
}

// AudioSlice defines the sample and time boundaries of an audio slice.
type AudioSlice struct {
	StartSample int64
	EndSample   int64
	StartTime   time.Duration
	EndTime     time.Duration
}

// FindSilenceSplitPoints scans 30ms frames of a WAV file to find natural split points
// between minChunk and maxChunk intervals.
// For each chunk interval, it looks for consecutive frames below threshold (speech pause).
// If no frame falls below threshold, it falls back to the minimum-energy frame in the window.
func FindSilenceSplitPoints(wavPath string, minChunk, maxChunk time.Duration, threshold float64) ([]time.Duration, error) {
	if threshold <= 0 {
		threshold = SpeechRMSThreshold
	}
	f, err := os.Open(wavPath)
	if err != nil {
		return nil, fmt.Errorf("audio: open %s: %w", wavPath, err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	dataAt, err := WAVDataOffset(f)
	if err != nil {
		return nil, err
	}
	totalSamples := (stat.Size() - dataAt) / 2
	if totalSamples <= 0 {
		return nil, nil
	}
	totalDuration := time.Duration(float64(totalSamples)/float64(DefaultSampleRate)) * time.Second
	if totalDuration <= maxChunk {
		return nil, nil
	}

	if _, err := f.Seek(dataAt, io.SeekStart); err != nil {
		return nil, fmt.Errorf("audio: seek: %w", err)
	}

	r := bufio.NewReaderSize(f, 64*1024)
	buf := make([]byte, FrameSamples*2)
	var frameRMS []float64

	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return nil, fmt.Errorf("audio: read frame: %w", err)
		}
		frameRMS = append(frameRMS, rmsOfPCM(buf))
	}

	if len(frameRMS) == 0 {
		return nil, nil
	}

	var splitPoints []time.Duration
	curStart := time.Duration(0)

	for curStart+maxChunk < totalDuration {
		searchStart := curStart + minChunk
		searchEnd := curStart + maxChunk
		if searchEnd > totalDuration {
			searchEnd = totalDuration
		}

		startFrame := int(searchStart / FrameDuration)
		endFrame := int(searchEnd / FrameDuration)
		if startFrame >= len(frameRMS) {
			break
		}
		if endFrame > len(frameRMS) {
			endFrame = len(frameRMS)
		}
		if startFrame >= endFrame {
			startFrame = endFrame - 1
			if startFrame < 0 {
				startFrame = 0
			}
		}

		bestFrame := -1
		minRMS := math.MaxFloat64

		longestRunStart := -1
		longestRunLen := 0
		curRunStart := -1
		curRunLen := 0

		for i := startFrame; i < endFrame; i++ {
			rms := frameRMS[i]
			if rms < minRMS {
				minRMS = rms
				bestFrame = i
			}
			if rms < threshold {
				if curRunStart == -1 {
					curRunStart = i
					curRunLen = 1
				} else {
					curRunLen++
				}
				if curRunLen > longestRunLen {
					longestRunLen = curRunLen
					longestRunStart = curRunStart
				}
			} else {
				curRunStart = -1
				curRunLen = 0
			}
		}

		splitFrame := bestFrame
		if longestRunLen >= 3 {
			// Center of longest silence run (at least 90ms)
			splitFrame = longestRunStart + longestRunLen/2
		}

		splitTime := time.Duration(splitFrame) * FrameDuration
		if splitTime <= curStart {
			splitTime = curStart + maxChunk
		}
		splitPoints = append(splitPoints, splitTime)
		curStart = splitTime
	}

	return splitPoints, nil
}

// SilenceSlices converts silence split points into contiguous AudioSlices.
func SilenceSlices(totalDuration time.Duration, splits []time.Duration) []AudioSlice {
	boundaries := append([]time.Duration{0}, splits...)
	boundaries = append(boundaries, totalDuration)
	var slices []AudioSlice
	for i := 0; i < len(boundaries)-1; i++ {
		t0 := boundaries[i]
		t1 := boundaries[i+1]
		s0 := int64(float64(t0) / float64(time.Second) * float64(DefaultSampleRate))
		s1 := int64(float64(t1) / float64(time.Second) * float64(DefaultSampleRate))
		slices = append(slices, AudioSlice{
			StartSample: s0,
			EndSample:   s1,
			StartTime:   t0,
			EndTime:     t1,
		})
	}
	return slices
}

// OverlapSlices divides totalDuration into sliding window AudioSlices with overlap.
func OverlapSlices(totalDuration time.Duration, window, overlap time.Duration) []AudioSlice {
	if totalDuration <= window {
		s1 := int64(float64(totalDuration) / float64(time.Second) * float64(DefaultSampleRate))
		return []AudioSlice{{
			StartSample: 0,
			EndSample:   s1,
			StartTime:   0,
			EndTime:     totalDuration,
		}}
	}
	stride := window - overlap
	if stride <= 0 {
		stride = window / 2
	}
	var slices []AudioSlice
	cur := time.Duration(0)
	for cur < totalDuration {
		end := cur + window
		if end > totalDuration {
			end = totalDuration
		}
		s0 := int64(float64(cur) / float64(time.Second) * float64(DefaultSampleRate))
		s1 := int64(float64(end) / float64(time.Second) * float64(DefaultSampleRate))
		slices = append(slices, AudioSlice{
			StartSample: s0,
			EndSample:   s1,
			StartTime:   cur,
			EndTime:     end,
		})
		if end >= totalDuration {
			break
		}
		cur += stride
	}
	return slices
}
