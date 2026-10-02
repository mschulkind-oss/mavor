//go:build gnome

package gnome

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mschulkind-oss/mavor/test/desktop"
)

func TestGNOMEDaemonInitializationLifecycle(t *testing.T) {
	if os.Getenv("MAVOR_READINESS_MODEL_DIR") == "" {
		t.Skip("existing mounted models required")
	}
	binary, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	manifest := filepath.Join(t.TempDir(), "lifecycle.json")
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", "supervise.py", "python3", "lifecycle_harness.py", binary, manifest)
	raw, e := cmd.CombinedOutput()
	t.Log(string(raw))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = os.ReadFile(manifest)
	if e != nil {
		t.Fatal(e)
	}
	var evidence []desktop.Evidence
	if e = json.Unmarshal(raw, &evidence); e != nil {
		t.Fatal(e)
	}
	if len(evidence) != 8 {
		t.Fatal("missing lifecycle captures")
	}
	for _, capture := range evidence[1:] {
		if e = desktop.ValidateArtifact(evidence[0].File, capture); e != nil {
			t.Fatalf("%s: %v", capture.ID, e)
		}
	}
}
