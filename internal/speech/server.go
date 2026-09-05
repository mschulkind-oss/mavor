package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ServerTranscriber sends audio recordings to a warm local whisper-server or OpenAI-compatible
// speech-to-text HTTP/Unix socket server.
type ServerTranscriber struct {
	// Endpoint is either a Unix domain socket path (e.g. "/run/user/1000/mavor-server.sock", "unix:///tmp/mavor.sock")
	// or an HTTP/HTTPS URL (e.g. "http://localhost:8080").
	Endpoint string

	// Model is the model name to pass in the request (e.g. "base.en").
	Model string

	// Client is the http.Client used to send requests. If nil, one is created automatically.
	Client *http.Client

	// Supervisor is an optional child server supervisor.
	Supervisor *Supervisor

	// Logger is the structured logger.
	Logger *slog.Logger

	// mu guards learnedURL, which is the request URL a previous call found
	// the server answering on. Discovering it costs one 404, and paying that
	// on every utterance would put it in the latency of every dictation.
	mu         sync.Mutex
	learnedURL string
}

// NewServerTranscriber creates a new ServerTranscriber pointing to endpoint.
func NewServerTranscriber(endpoint string) *ServerTranscriber {
	return &ServerTranscriber{
		Endpoint: endpoint,
		Logger:   slog.Default(),
	}
}

// Start ensures the child server process is running if a Supervisor is configured.
func (s *ServerTranscriber) Start(ctx context.Context) error {
	if s.Supervisor != nil {
		return s.Supervisor.Start(ctx)
	}
	return nil
}

// Close stops the child server process if a Supervisor is configured.
func (s *ServerTranscriber) Close() error {
	if s.Supervisor != nil {
		return s.Supervisor.Stop()
	}
	return nil
}

// Transcribe sends the WAV file at wavPath to the server and returns the transcribed text.
func (s *ServerTranscriber) Transcribe(ctx context.Context, wavPath string) (string, error) {
	log := s.Logger
	if log == nil {
		log = slog.Default()
	}

	// Auto-start supervisor if supervised server is not running
	if s.Supervisor != nil && !s.Supervisor.IsRunning() {
		log.Info("speech: server not running; starting via supervisor")
		if err := s.Supervisor.Start(ctx); err != nil {
			return "", fmt.Errorf("speech: start server: %w", err)
		}
	}

	file, err := os.Open(wavPath)
	if err != nil {
		return "", fmt.Errorf("speech: server: open wav %s: %w", wavPath, err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	part, err := writer.CreateFormFile("file", filepath.Base(wavPath))
	if err != nil {
		return "", fmt.Errorf("speech: server: create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("speech: server: copy audio data: %w", err)
	}

	if s.Model != "" {
		if err := writer.WriteField("model", s.Model); err != nil {
			return "", fmt.Errorf("speech: server: write model field: %w", err)
		}
	}
	if err := writer.WriteField("response_format", "json"); err != nil {
		return "", fmt.Errorf("speech: server: write response_format field: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("speech: server: close multipart writer: %w", err)
	}

	client := s.Client
	if client == nil {
		client = s.buildClient()
	}
	contentType := writer.FormDataContentType()
	payload := body.Bytes()

	// Two families of server answer this call on two different paths, and
	// nothing in the endpoint says which one is listening: whisper.cpp serves
	// /inference, everything OpenAI-compatible serves /v1/audio/transcriptions.
	// Try in turn and keep the one that answers.
	candidates := s.candidateURLs()
	var respBytes []byte
	var lastStatus int
	for i, reqURL := range candidates {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(payload))
		if err != nil {
			return "", fmt.Errorf("speech: server: new request: %w", err)
		}
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Accept", "application/json, text/plain, */*")

		log.Info("speech: sending transcription request to server",
			"endpoint", s.Endpoint,
			"url", reqURL,
			"model", s.Model,
			"wav", wavPath,
			"body_bytes", len(payload),
		)

		start := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			return "", fmt.Errorf("speech: server request failed: %w", err)
		}
		elapsed := time.Since(start)
		respBytes, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return "", fmt.Errorf("speech: server: read response: %w", err)
		}
		lastStatus = resp.StatusCode

		log.Info("speech: server response received",
			"status", resp.StatusCode,
			"duration_ms", elapsed.Milliseconds(),
			"body_len", len(respBytes),
		)

		if wrongPath(resp.StatusCode) && i < len(candidates)-1 {
			log.Info("speech: server does not serve this path; trying the next one",
				"url", reqURL, "status", resp.StatusCode)
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("speech: server returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}
		s.rememberURL(reqURL)
		break
	}
	if lastStatus == 0 {
		return "", errors.New("speech: server: no endpoint to send the request to")
	}

	// Try unmarshaling OpenAI-format JSON response
	var jsonResp struct {
		Text  *string         `json:"text"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(respBytes, &jsonResp); err == nil {
		if len(jsonResp.Error) > 0 && string(jsonResp.Error) != "null" {
			return "", fmt.Errorf("speech: server error: %s", string(jsonResp.Error))
		}
		if jsonResp.Text != nil {
			return strings.TrimSpace(*jsonResp.Text), nil
		}
	}

	// Fall back to plain text response
	return strings.TrimSpace(string(respBytes)), nil
}

// knownPaths are the two request paths a transcription server answers on.
// whisper.cpp's own server is first because it is the one mavor supervises;
// the OpenAI path is what every hosted-compatible server uses.
var knownPaths = []string{"/inference", "/v1/audio/transcriptions"}

// candidateURLs is the ordered list of URLs to try. A path the user wrote
// themselves is the only candidate — a 404 from it is an error to report, not
// a reason to go looking around someone else's server.
func (s *ServerTranscriber) candidateURLs() []string {
	s.mu.Lock()
	learned := s.learnedURL
	s.mu.Unlock()
	if learned != "" {
		return []string{learned}
	}

	base := s.baseURL()
	for _, p := range knownPaths {
		if strings.HasSuffix(base, p) {
			return []string{base}
		}
	}
	urls := make([]string, 0, len(knownPaths))
	for _, p := range knownPaths {
		urls = append(urls, base+p)
	}
	return urls
}

// baseURL turns the configured endpoint into an origin the http client can
// use. A Unix socket has no host, so it gets a placeholder: the dialer in
// buildClient ignores it and connects to the socket path.
func (s *ServerTranscriber) baseURL() string {
	endpoint := s.target()
	if isUnix, _ := IsUnixSocket(endpoint); isUnix {
		return "http://localhost"
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}
	return strings.TrimRight(endpoint, "/")
}

// target is where requests actually go.
func (s *ServerTranscriber) target() string {
	return s.Endpoint
}

func (s *ServerTranscriber) rememberURL(u string) {
	s.mu.Lock()
	s.learnedURL = u
	s.mu.Unlock()
}

// wrongPath reports whether a status means "not here" rather than "this
// failed". Only those two are worth retrying elsewhere; a 500 from the right
// endpoint is a server problem and trying another path would hide it.
func wrongPath(status int) bool {
	return status == http.StatusNotFound || status == http.StatusMethodNotAllowed
}

func (s *ServerTranscriber) buildClient() *http.Client {
	isUnix, socketPath := IsUnixSocket(s.target())
	if isUnix {
		transport := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}
		return &http.Client{Transport: transport}
	}
	return &http.Client{Transport: http.DefaultTransport}
}
