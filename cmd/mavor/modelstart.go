package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/daemon"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

// starter is the optional interface a transcriber implements when it has a
// model or a subprocess to bring up before it can decode.
type starter interface {
	Start(context.Context) error
}

// initializeModels owns both independent loads until transfer to the daemon.
// Failure cancels the sibling and joins it before closing any native resources.
func initializeModels(ctx context.Context, cfg config.Config, logger *slog.Logger) (daemon.Initialized, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type mainResult struct {
		t   speech.Transcriber
		err error
	}
	mainDone := make(chan mainResult, 1)
	go func() {
		res, e := speech.Resolve(cfg)
		if e != nil {
			cancel()
			mainDone <- mainResult{err: e}
			return
		}
		logger.Info("models: resolved", "model", cfg.Model, "runtime", res.Runtime, "placement", res.Placement, "placement_reason", res.Reason, "model_path", res.ModelPath, "model_dir", res.ModelDir)
		t, e := speech.FactoryFor(cfg, res, logger)
		if e == nil {
			if st, ok := t.(starter); ok {
				e = st.Start(ctx)
			}
		}
		if e == nil {
			probe, stop := context.WithTimeout(ctx, 30*time.Second)
			e = speech.VerifyReadiness(probe, t)
			stop()
		}
		if e != nil {
			cancel()
		}
		mainDone <- mainResult{t, e}
	}()
	preview, e := speech.LoadPreview(ctx, cfg, logger)
	if e == nil && preview.Companion != nil {
		probe, stop := context.WithTimeout(ctx, 30*time.Second)
		e = speech.VerifyReadiness(probe, preview.Companion)
		stop()
	}
	if e != nil {
		cancel()
	}
	main := <-mainDone
	if main.err != nil || e != nil || ctx.Err() != nil {
		if c, ok := main.t.(io.Closer); ok {
			_ = c.Close()
		}
		preview.Close()
		if failure := errors.Join(main.err, e); failure != nil {
			return daemon.Initialized{}, failure
		}
		return daemon.Initialized{}, ctx.Err()
	}
	logger.Info("models: inference verified", "main", cfg.Model, "companion", preview.Model)
	return daemon.Initialized{Transcriber: main.t, PreviewMode: preview.Mode, PreviewCompanion: preview.Companion, MainModel: cfg.Model, CompanionModel: preview.Model}, nil
}
