package speech

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServerTranscriberHTTPJSONSuccess(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			http.Error(w, "expected multipart form", http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "missing file field", http.StatusBadRequest)
			return
		}
		defer file.Close()
		content, _ := io.ReadAll(file)
		if string(content) != "fake-wav-data" {
			http.Error(w, "bad file content", http.StatusBadRequest)
			return
		}
		if header.Filename != "audio.wav" {
			http.Error(w, fmt.Sprintf("bad filename %q", header.Filename), http.StatusBadRequest)
			return
		}
		if r.FormValue("model") != "tiny.en" {
			http.Error(w, fmt.Sprintf("bad model %q", r.FormValue("model")), http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text": "  hello world from test server  "}`)
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	st.Model = "tiny.en"

	got, err := st.Transcribe(context.Background(), wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "hello world from test server" {
		t.Fatalf("got %q, want %q", got, "hello world from test server")
	}
}

func TestServerTranscriberUnixSocketSuccess(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	sockPath := filepath.Join(t.TempDir(), "test-server.sock")
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/audio/transcriptions" {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"text": "unix socket transcription"}`)
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	st := NewServerTranscriber(sockPath)
	got, err := st.Transcribe(context.Background(), wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "unix socket transcription" {
		t.Fatalf("got %q, want %q", got, "unix socket transcription")
	}
}

func TestServerTranscriberPlainTextSuccess(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "   plain text transcription output   \n")
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	got, err := st.Transcribe(context.Background(), wavPath)
	if err != nil {
		t.Fatalf("Transcribe failed: %v", err)
	}
	if got != "plain text transcription output" {
		t.Fatalf("got %q, want %q", got, "plain text transcription output")
	}
}

func TestServerTranscriberHTTPError(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error: model collapsed", http.StatusInternalServerError)
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	_, err := st.Transcribe(context.Background(), wavPath)
	if err == nil {
		t.Fatal("expected error on 500 status")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("error %q should mention status 500", err)
	}
}

func TestServerTranscriberJSONErrorPayload(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"error": {"message": "invalid model parameter"}}`)
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	_, err := st.Transcribe(context.Background(), wavPath)
	if err == nil {
		t.Fatal("expected error on error JSON payload")
	}
	if !strings.Contains(err.Error(), "invalid model parameter") {
		t.Fatalf("error %q should mention invalid model parameter", err)
	}
}

func TestServerTranscriberMissingWav(t *testing.T) {
	st := NewServerTranscriber("http://127.0.0.1:9999")
	_, err := st.Transcribe(context.Background(), "/does/not/exist.wav")
	if err == nil {
		t.Fatal("expected error on non-existent file")
	}
}

func TestServerTranscriberContextTimeout(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644)

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		fmt.Fprint(w, `{"text": "done"}`)
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := st.Transcribe(ctx, wavPath)
	if err == nil {
		t.Fatal("expected error on context timeout")
	}
}

func TestServerTranscriberSupervisorLifecycle(t *testing.T) {
	sockPath := filepath.Join(t.TempDir(), "server-lifecycle.sock")
	sup := NewSupervisor(SupervisorConfig{
		ServerSocket: sockPath,
		PollInterval: 10 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
		CommandFunc:  whisperServerCommand,
	})

	st := NewServerTranscriber(sockPath)
	st.Supervisor = sup

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := st.Start(ctx); err != nil {
		t.Fatalf("st.Start: %v", err)
	}
	if !sup.IsRunning() {
		t.Fatal("expected supervisor to be running")
	}

	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	_ = os.WriteFile(wavPath, []byte("fake-wav"), 0o644)
	got, err := st.Transcribe(ctx, wavPath)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got != "fake server transcript" {
		t.Fatalf("got %q, want fake server transcript", got)
	}

	if err := st.Close(); err != nil {
		t.Fatalf("st.Close: %v", err)
	}
	if sup.IsRunning() {
		t.Fatal("expected supervisor to be stopped after Close")
	}
}

// whisperCppServer is a stand-in for whisper.cpp's own server: it serves
// `/inference` and returns 404 for everything else, which is what a real
// `whisper-server` does to the OpenAI path the client used to assume.
func whisperCppServer(t *testing.T, seen *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/inference" {
			http.Error(w, "File Not Found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text": "whisper cpp transcript"}`)
	}))
}

func TestServerTranscriberFallsBackToInferencePath(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []string
	ts := whisperCppServer(t, &seen)
	defer ts.Close()

	// A bare host and port, which is what a user writes in config.toml.
	st := NewServerTranscriber(ts.URL)
	got, err := st.Transcribe(context.Background(), wavPath)
	if err != nil {
		t.Fatalf("Transcribe against a whisper.cpp server failed: %v", err)
	}
	if got != "whisper cpp transcript" {
		t.Fatalf("got %q, want the transcript from /inference", got)
	}
	if len(seen) == 0 || seen[len(seen)-1] != "/inference" {
		t.Fatalf("request paths were %v; the last one should be /inference", seen)
	}
}

func TestServerTranscriberRemembersTheWorkingPath(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// An OpenAI-compatible server: the path mavor tries first is the wrong
	// one here, so the discovery miss is visible.
	var mu sync.Mutex
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"text": "hosted transcript"}`)
	}))
	defer ts.Close()

	st := NewServerTranscriber(ts.URL)
	for i := 0; i < 3; i++ {
		if _, err := st.Transcribe(context.Background(), wavPath); err != nil {
			t.Fatalf("Transcribe %d failed: %v", i, err)
		}
	}
	// One miss on the first call, then never again. Paying the 404 on every
	// dictation would put it in the latency of every utterance.
	misses := 0
	for _, p := range seen {
		if p != "/v1/audio/transcriptions" {
			misses++
		}
	}
	if misses != 1 {
		t.Fatalf("request paths were %v; want exactly one miss before the path is learned", seen)
	}
}

func TestServerTranscriberDoesNotSecondGuessAnExplicitPath(t *testing.T) {
	wavPath := filepath.Join(t.TempDir(), "audio.wav")
	if err := os.WriteFile(wavPath, []byte("fake-wav-data"), 0o644); err != nil {
		t.Fatal(err)
	}
	var seen []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.URL.Path)
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer ts.Close()

	// The user named a path. A 404 from it is an error to report, not an
	// invitation to go looking for a different endpoint on their server.
	st := NewServerTranscriber(ts.URL + "/v1/audio/transcriptions")
	if _, err := st.Transcribe(context.Background(), wavPath); err == nil {
		t.Fatal("expected the 404 to be reported, not worked around")
	}
	if len(seen) != 1 {
		t.Fatalf("request paths were %v; an explicit path is tried once", seen)
	}
}
