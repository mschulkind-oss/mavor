package speech

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	reqURL := s.resolveURL()
	if client == nil {
		client = s.buildClient()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, &body)
	if err != nil {
		return "", fmt.Errorf("speech: server: new request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Accept", "application/json, text/plain, */*")

	log.Info("speech: sending transcription request to server",
		"endpoint", s.Endpoint,
		"url", reqURL,
		"model", s.Model,
		"wav", wavPath,
		"body_bytes", body.Len(),
	)

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("speech: server request failed: %w", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(start)

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("speech: server: read response: %w", err)
	}

	log.Info("speech: server response received",
		"status", resp.StatusCode,
		"duration_ms", elapsed.Milliseconds(),
		"body_len", len(respBytes),
	)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("speech: server returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
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

func (s *ServerTranscriber) resolveURL() string {
	endpoint := s.Endpoint
	isUnix, _ := IsUnixSocket(endpoint)
	if isUnix {
		return "http://localhost/v1/audio/transcriptions"
	}

	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "http://" + endpoint
	}

	trimmed := strings.TrimRight(endpoint, "/")
	if strings.HasSuffix(trimmed, "/v1/audio/transcriptions") || strings.HasSuffix(trimmed, "/inference") {
		return trimmed
	}
	return trimmed + "/v1/audio/transcriptions"
}

func (s *ServerTranscriber) buildClient() *http.Client {
	isUnix, socketPath := IsUnixSocket(s.Endpoint)
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
