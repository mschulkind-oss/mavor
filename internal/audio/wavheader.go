package audio

import (
	"encoding/binary"
	"fmt"
	"io"
)

// A WAV header is not 44 bytes.
//
// It is 44 for the minimal RIFF/fmt/data layout that most examples show, and
// mavor assumed that everywhere it read a recording. Real writers disagree:
// ffmpeg emits a LIST/INFO chunk and puts the samples at byte 78, and
// libsndfile — which is what parec writes through — does the same. Reading
// from 44 then feeds tens of bytes of chunk headers to the recogniser as
// audio, and if the true offset is odd from there, every 16-bit sample is
// split across the wrong pair of bytes and the whole stream is noise.
//
// The final transcript never showed it, because whisper-cli parses the file
// properly. Only the paths that read the growing file themselves were
// affected: the live preview and the level meter.

// wavHeaderScanLimit bounds how far into a file the data chunk is looked for.
// A header is a few dozen bytes; anything past this is not a header we
// understand, and walking a corrupt file forever is worse than failing.
const wavHeaderScanLimit = 64 * 1024

// WAVDataOffset returns the byte offset where samples begin.
//
// It walks the RIFF chunk list rather than assuming a layout. The data chunk's
// declared SIZE is deliberately ignored: while parec is still recording, that
// field holds a placeholder and is corrected only when the file is closed.
// The offset is what matters and it is correct from the first write.
func WAVDataOffset(r io.ReaderAt) (int64, error) {
	var riff [12]byte
	if _, err := r.ReadAt(riff[:], 0); err != nil {
		return 0, fmt.Errorf("audio: read RIFF header: %w", err)
	}
	if string(riff[0:4]) != "RIFF" || string(riff[8:12]) != "WAVE" {
		return 0, fmt.Errorf("audio: not a RIFF/WAVE file")
	}

	pos := int64(12)
	var hdr [8]byte
	for pos < wavHeaderScanLimit {
		if _, err := r.ReadAt(hdr[:], pos); err != nil {
			return 0, fmt.Errorf("audio: no data chunk found: %w", err)
		}
		id := string(hdr[0:4])
		size := int64(binary.LittleEndian.Uint32(hdr[4:8]))
		if id == "data" {
			return pos + 8, nil
		}
		// Chunks are word-aligned: an odd size carries a pad byte that is
		// not counted in the size field.
		pos += 8 + size + (size & 1)
	}
	return 0, fmt.Errorf("audio: no data chunk within %d bytes", wavHeaderScanLimit)
}
