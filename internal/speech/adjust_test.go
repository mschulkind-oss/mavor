package speech

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/models"
)

// pathWithout points PATH at an empty directory, so no whisper server binary
// can be found however the host is provisioned.
func pathWithout(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// pathWith puts a fake, executable whisper-server on PATH.
func pathWith(t *testing.T, name string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	t.Setenv("PATH", dir)
}

func whisperLocalServer() models.Selection {
	return models.Selection{
		Runtime:   models.RuntimeWhisper,
		Placement: models.PlacementLocalServer,
		Reason:    "derived",
	}
}

func TestLocalServerFallsBackToSubprocessWhenNoServerBinary(t *testing.T) {
	pathWithout(t)
	got := AdjustForEnvironment(whisperLocalServer())
	if got.Placement != models.PlacementSubprocess {
		t.Fatalf("placement = %q, want %q", got.Placement, models.PlacementSubprocess)
	}
	if len(got.Warnings) == 0 {
		t.Fatal("a silent downgrade is the one thing this must not do: no warning")
	}
	if !strings.Contains(strings.Join(got.Warnings, " "), "whisper-server") {
		t.Errorf("warning does not name the missing binary: %v", got.Warnings)
	}
}

func TestLocalServerSurvivesWhenServerBinaryIsPresent(t *testing.T) {
	for _, name := range serverBinaryNames {
		t.Run(name, func(t *testing.T) {
			pathWith(t, name)
			got := AdjustForEnvironment(whisperLocalServer())
			if got.Placement != models.PlacementLocalServer {
				t.Errorf("placement = %q, want it left alone at %q", got.Placement, models.PlacementLocalServer)
			}
			if len(got.Warnings) != 0 {
				t.Errorf("unexpected warnings: %v", got.Warnings)
			}
		})
	}
}

// A placement the user named themselves must fail loudly rather than being
// quietly rewritten underneath them.
func TestExplicitSubprocessAndRemoteAreNotAdjusted(t *testing.T) {
	pathWithout(t)
	for _, p := range []models.Placement{models.PlacementSubprocess, models.PlacementRemote} {
		sel := whisperLocalServer()
		sel.Placement = p
		if got := AdjustForEnvironment(sel); got.Placement != p {
			t.Errorf("placement %q was rewritten to %q", p, got.Placement)
		}
	}
}

// Sherpa runs in-process and never wants a whisper server, so a missing one is
// not its problem.
func TestSherpaInProcessIsNeverAdjusted(t *testing.T) {
	pathWithout(t)
	sel := models.Selection{Runtime: models.RuntimeSherpa, Placement: models.PlacementInProcess}
	got := AdjustForEnvironment(sel)
	if got.Placement != models.PlacementInProcess || len(got.Warnings) != 0 {
		t.Errorf("sherpa selection was adjusted: %+v", got)
	}
}
