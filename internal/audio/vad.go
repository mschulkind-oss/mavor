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

const (
	// DefaultSampleRate for whisper input (16 kHz).
	DefaultSampleRate = 16000
	// FrameDuration is the standard 30ms evaluation window for VAD.
	FrameDuration = 30 * time.Millisecond
	// FrameSamples is 16000 * 0.030 = 480 samples per 30ms frame.
	FrameSamples = 480
	// SpeechRMSThreshold is the normalized amplitude threshold [0, 1] distinguishing
	// human speech from ambient line noise / room silence.
	SpeechRMSThreshold = 0.012
)

// DetectSpeech analyzes a 16kHz 16-bit mono WAV file to determine whether
// it contains at least minSpeech duration of active voice. If the total
// speech duration is below minSpeech, it returns false (indicating silence
// or non-vocal background noise).
//
// It streams the file a frame at a time rather than reading it whole. The
// answer is a running count of loud frames, so nothing here needs two copies
// of the recording in memory — which is what materializing it cost: a 120s
// dictation allocated 7.7 MB (the PCM bytes, then the same samples again as
// int16) to produce one bool, on the critical path between the user releasing
// the key and Transcribe being called.
func DetectSpeech(wavPath string, minSpeech time.Duration) (bool, error) {
	f, err := os.Open(wavPath)
	if err != nil {
		return false, fmt.Errorf("vad: open %s: %w", wavPath, err)
	}
	defer f.Close()

	dataAt, err := WAVDataOffset(f)
	if err != nil {
		// A file too short to hold a header yet is not an error: parec has
		// simply not written one. A file that is not RIFF/WAVE at all is.
		if stat, statErr := f.Stat(); statErr == nil && stat.Size() < 44 {
			return false, nil
		}
		return false, fmt.Errorf("vad: %s: %w", wavPath, err)
	}
	if _, err := f.Seek(dataAt, io.SeekStart); err != nil {
		return false, fmt.Errorf("vad: seek to samples: %w", err)
	}

	// One frame-sized buffer, reused for the whole file. io.ReadFull fills it
	// or reports that the tail is short — and a partial trailing frame is
	// dropped, exactly as the whole-slice loop in SpeechDuration does.
	r := bufio.NewReaderSize(f, 64*1024)
	buf := make([]byte, FrameSamples*2)
	speech := time.Duration(0)
	for {
		if _, err := io.ReadFull(r, buf); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return false, fmt.Errorf("vad: read pcm: %w", err)
		}
		if rmsOfPCM(buf) >= SpeechRMSThreshold {
			speech += FrameDuration
			if speech >= minSpeech {
				// Nothing later can lower the count, so the rest of the file
				// cannot change the answer.
				return true, nil
			}
		}
	}
	return speech >= minSpeech, nil
}

// rmsOfPCM is CalculateRMS over little-endian s16 bytes, so a caller that has
// the raw frame need not convert it to []int16 first.
func rmsOfPCM(frame []byte) float64 {
	if len(frame) < 2 {
		return 0.0
	}
	var sum float64
	for i := 0; i+1 < len(frame); i += 2 {
		norm := float64(int16(binary.LittleEndian.Uint16(frame[i:i+2]))) / 32768.0
		sum += norm * norm
	}
	return math.Sqrt(sum / float64(len(frame)/2))
}

// ReadWAVSamples parses a 16-bit mono PCM WAV file and returns its raw audio samples.
func ReadWAVSamples(path string) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("vad: open %s: %w", path, err)
	}
	defer f.Close()

	dataAt, err := WAVDataOffset(f)
	if err != nil {
		// A file too short to hold a header yet is not an error: parec has
		// simply not written one. A file that is not RIFF/WAVE at all is.
		stat, statErr := f.Stat()
		if statErr == nil && stat.Size() < 44 {
			return nil, nil
		}
		return nil, fmt.Errorf("vad: %s: %w", path, err)
	}

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	dataSize := stat.Size() - dataAt
	if dataSize <= 0 {
		return nil, nil
	}
	if _, err := f.Seek(dataAt, io.SeekStart); err != nil {
		return nil, fmt.Errorf("vad: seek to samples: %w", err)
	}

	rawBytes := make([]byte, dataSize)
	n, err := io.ReadFull(f, rawBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("vad: read pcm: %w", err)
	}

	sampleCount := n / 2
	samples := make([]int16, sampleCount)
	for i := 0; i < sampleCount; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(rawBytes[i*2 : i*2+2]))
	}
	return samples, nil
}

// SpeechDuration calculates the total duration of frames exceeding the RMS threshold.
func SpeechDuration(samples []int16, sampleRate int, threshold float64) time.Duration {
	if len(samples) == 0 || sampleRate <= 0 {
		return 0
	}
	frameSize := (sampleRate * 30) / 1000 // 30ms
	if frameSize <= 0 {
		frameSize = 480
	}

	activeFrames := 0
	for i := 0; i+frameSize <= len(samples); i += frameSize {
		rms := CalculateRMS(samples[i : i+frameSize])
		if rms >= threshold {
			activeFrames++
		}
	}
	return time.Duration(activeFrames) * 30 * time.Millisecond
}

// CalculateRMS calculates the Root Mean Square energy normalized to [0.0, 1.0].
func CalculateRMS(frame []int16) float64 {
	if len(frame) == 0 {
		return 0.0
	}
	var sum float64
	for _, s := range frame {
		norm := float64(s) / 32768.0
		sum += norm * norm
	}
	return math.Sqrt(sum / float64(len(frame)))
}

// ReadRecentSamples reads the last up to maxSamples from a 16-bit mono WAV file.
// It handles growing files safely during active recording.
func ReadRecentSamples(path string, maxSamples int) ([]int16, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	dataAt, err := WAVDataOffset(f)
	if err != nil {
		return nil, nil
	}
	if stat.Size() <= dataAt {
		return nil, nil
	}
	dataSize := stat.Size() - dataAt
	bytesToRead := int64(maxSamples * 2)
	if bytesToRead > dataSize {
		bytesToRead = dataSize
	}
	bytesToRead -= bytesToRead % 2
	if bytesToRead <= 0 {
		return nil, nil
	}

	offset := stat.Size() - bytesToRead
	buf := make([]byte, bytesToRead)
	n, err := f.ReadAt(buf, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	sampleCount := n / 2
	samples := make([]int16, sampleCount)
	for i := 0; i < sampleCount; i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(buf[i*2 : i*2+2]))
	}
	return samples, nil
}

// WriteWAV writes 16-bit mono PCM samples to a RIFF/WAVE file.
func WriteWAV(path string, samples []int16, sampleRate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dataSize := len(samples) * 2
	fileSize := 36 + dataSize

	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(fileSize))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16) // PCM format size
	binary.LittleEndian.PutUint16(header[20:22], 1)  // PCM format
	binary.LittleEndian.PutUint16(header[22:24], 1)  // Mono
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*2)) // Byte rate
	binary.LittleEndian.PutUint16(header[32:34], 2)                    // Block align
	binary.LittleEndian.PutUint16(header[34:36], 16)                   // Bits per sample
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))

	if _, err := f.Write(header); err != nil {
		return err
	}

	buf := make([]byte, dataSize)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[i*2:i*2+2], uint16(s))
	}
	_, err = f.Write(buf)
	return err
}
