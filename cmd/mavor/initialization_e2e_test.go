//go:build e2e

package main

import (
	"context"
	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/speech"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestProductionInitializationTransfersVerifiedMountedModels(t *testing.T) {
	root := os.Getenv("MAVOR_READINESS_MODEL_DIR")
	if root == "" {
		t.Skip("set MAVOR_READINESS_MODEL_DIR to existing mounted models")
	}
	cfg := config.Default()
	cfg.Model = "whisper-base.en"
	cfg.Paths.Models = root
	cfg.Advanced.GPU = "off"
	cfg.Advanced.Threads = 2
	cfg.Preview.Enabled = true
	cfg.Preview.Source = "zipformer-streaming-20m"
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	loaded, e := initializeModels(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cancel()
	if e != nil {
		t.Fatal(e)
	}
	if c, ok := loaded.Transcriber.(io.Closer); ok {
		defer c.Close()
	}
	if c, ok := loaded.PreviewCompanion.(io.Closer); ok {
		defer c.Close()
	}
	if loaded.PreviewCompanion == nil || loaded.MainModel != cfg.Model || loaded.CompanionModel != cfg.Preview.Source {
		t.Fatalf("missing transferred model: %+v", loaded)
	}
	server, ok := speech.Unwrap(loaded.Transcriber).(*speech.ServerTranscriber)
	if !ok {
		t.Fatalf("main=%T", loaded.Transcriber)
	}
	if !server.Supervisor.IsRunning() {
		t.Fatal("verified child died with initialization context")
	}
	// Use a fresh operation after transfer; models stay loaded and streams reset.
	probe, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	if e := speech.VerifyReadiness(probe, loaded.Transcriber); e != nil {
		t.Fatal(e)
	}
	if e := speech.VerifyReadiness(probe, loaded.PreviewCompanion); e != nil {
		t.Fatal(e)
	}
}
