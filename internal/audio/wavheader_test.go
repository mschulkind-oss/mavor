package audio

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// buildWAV writes a RIFF/WAVE file with the given extra chunks BEFORE the data
// chunk, which is how real writers differ from the 44-byte textbook layout.
func buildWAV(t *testing.T, samples []int16, extra ...struct {
	id   string
	body []byte
}) []byte {
	t.Helper()
	var body bytes.Buffer
	body.WriteString("WAVE")

	fmtChunk := make([]byte, 16)
	binary.LittleEndian.PutUint16(fmtChunk[0:], 1)     // PCM
	binary.LittleEndian.PutUint16(fmtChunk[2:], 1)     // mono
	binary.LittleEndian.PutUint32(fmtChunk[4:], 16000) // rate
	binary.LittleEndian.PutUint32(fmtChunk[8:], 32000) // byte rate
	binary.LittleEndian.PutUint16(fmtChunk[12:], 2)    // block align
	binary.LittleEndian.PutUint16(fmtChunk[14:], 16)   // bits
	writeChunk(&body, "fmt ", fmtChunk)

	for _, e := range extra {
		writeChunk(&body, e.id, e.body)
	}

	var data bytes.Buffer
	for _, s := range samples {
		_ = binary.Write(&data, binary.LittleEndian, s)
	}
	writeChunk(&body, "data", data.Bytes())

	var out bytes.Buffer
	out.WriteString("RIFF")
	_ = binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func writeChunk(w *bytes.Buffer, id string, body []byte) {
	w.WriteString(id)
	_ = binary.Write(w, binary.LittleEndian, uint32(len(body)))
	w.Write(body)
	if len(body)%2 == 1 {
		w.WriteByte(0) // chunks are word-aligned
	}
}

// The textbook layout, where the old hardcoded 44 happened to be right.
func TestDataOffsetOfAMinimalWAV(t *testing.T) {
	f := buildWAV(t, []int16{1, 2, 3})
	got, err := WAVDataOffset(bytes.NewReader(f))
	if err != nil {
		t.Fatalf("WAVDataOffset: %v", err)
	}
	if got != 44 {
		t.Errorf("offset = %d, want 44 for a minimal RIFF/fmt/data file", got)
	}
}

// ffmpeg writes exactly this, and puts the samples at 78. libsndfile — which
// parec writes through — does the same. Reading from 44 here hands 34 bytes of
// chunk header to the recogniser as audio.
func TestDataOffsetSkipsAListChunk(t *testing.T) {
	list := struct {
		id   string
		body []byte
	}{"LIST", []byte("INFOISFT\x0e\x00\x00\x00Lavf62.3.100\x00")}

	f := buildWAV(t, []int16{1, 2, 3}, list)
	got, err := WAVDataOffset(bytes.NewReader(f))
	if err != nil {
		t.Fatalf("WAVDataOffset: %v", err)
	}
	if got == 44 {
		t.Fatal("offset came back as 44 with a LIST chunk present — the header was not parsed")
	}
	// Prove it points at the samples rather than at header bytes.
	var first int16
	if err := binary.Read(bytes.NewReader(f[got:]), binary.LittleEndian, &first); err != nil {
		t.Fatal(err)
	}
	if first != 1 {
		t.Errorf("first sample = %d, want 1 — the offset points into the header", first)
	}
}

// An odd-sized chunk carries a pad byte that its size field does not count.
// Miss it and every later offset is wrong by one, which splits every 16-bit
// sample across the wrong pair of bytes and turns the audio into noise.
func TestDataOffsetHandlesOddSizedChunkPadding(t *testing.T) {
	odd := struct {
		id   string
		body []byte
	}{"junk", []byte("odd")} // 3 bytes, so one pad byte follows

	f := buildWAV(t, []int16{7, 8}, odd)
	got, err := WAVDataOffset(bytes.NewReader(f))
	if err != nil {
		t.Fatalf("WAVDataOffset: %v", err)
	}
	var first int16
	if err := binary.Read(bytes.NewReader(f[got:]), binary.LittleEndian, &first); err != nil {
		t.Fatal(err)
	}
	if first != 7 {
		t.Errorf("first sample = %d, want 7 — the pad byte was not accounted for", first)
	}
}

func TestNonWAVIsRejected(t *testing.T) {
	if _, err := WAVDataOffset(bytes.NewReader([]byte("not a wav file at all!!!"))); err == nil {
		t.Error("a non-RIFF file was accepted")
	}
}

// The level meter and the VAD read the same growing file, so they must find
// the samples in a real-world header too.
func TestReadRecentSamplesFindsSamplesPastAListChunk(t *testing.T) {
	list := struct {
		id   string
		body []byte
	}{"LIST", []byte("INFOISFT\x08\x00\x00\x00parec\x00\x00\x00")}

	want := []int16{100, 200, 300, 400}
	path := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(path, buildWAV(t, want, list), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := ReadRecentSamples(path, len(want))
	if err != nil {
		t.Fatalf("ReadRecentSamples: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("samples = %v, want %v — the header was read as audio", got, want)
		}
	}
}

// ReadChunk is the preview's own reader: it tails the file parec is still
// writing. Feeding it header bytes as audio is what garbles the start of a
// live preview while the final transcript, which whisper parses properly,
// comes out fine.
func TestReadChunkStartsAtTheSamplesNotAtByte44(t *testing.T) {
	list := struct {
		id   string
		body []byte
	}{"LIST", []byte("INFOISFT\x0e\x00\x00\x00Lavf62.3.100\x00")}

	want := []int16{11, 22, 33, 44, 55}
	path := filepath.Join(t.TempDir(), "rec.wav")
	if err := os.WriteFile(path, buildWAV(t, want, list), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &ParecRecorder{outPath: path}
	chunk, err := r.ReadChunk()
	if err != nil {
		t.Fatalf("ReadChunk: %v", err)
	}
	if len(chunk) != len(want)*2 {
		t.Fatalf("read %d bytes, want %d — the offset is wrong", len(chunk), len(want)*2)
	}
	for i, w := range want {
		got := int16(binary.LittleEndian.Uint16(chunk[i*2:]))
		if got != w {
			t.Fatalf("sample %d = %d, want %d — reading from 44 hands the recogniser header bytes", i, got, w)
		}
	}
}
