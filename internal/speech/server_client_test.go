package speech

import (
	"net/http"
	"testing"
)

// The client is built once, so connections to a server on this same machine
// are kept alive between utterances instead of the socket being reopened for
// every dictation.
func TestTheServerClientIsBuiltOnce(t *testing.T) {
	s := &ServerTranscriber{Endpoint: "http://127.0.0.1:8080"}
	first := s.client()
	if first == nil {
		t.Fatal("client() returned nil")
	}
	if second := s.client(); second != first {
		t.Error("client() built a second http.Client: each utterance gets its own Transport and keeps no connection alive")
	}
}

// An injected client still wins, which is what the tests in this package and
// anyone pointing at a custom transport rely on.
func TestAnInjectedClientIsUsedAsIs(t *testing.T) {
	mine := &http.Client{}
	s := &ServerTranscriber{Endpoint: "http://127.0.0.1:8080", Client: mine}
	if got := s.client(); got != mine {
		t.Error("client() ignored the injected http.Client")
	}
}
