//go:build !sherpa

package speech

import (
	"fmt"
	"log/slog"

	"github.com/mschulkind-oss/mavor/internal/config"
)

func newSherpaTranscriber(_ config.Config, _ *slog.Logger) (Transcriber, error) {
	return nil, fmt.Errorf("speech: sherpa engine not supported in this build (requires cgo and -tags sherpa)")
}
