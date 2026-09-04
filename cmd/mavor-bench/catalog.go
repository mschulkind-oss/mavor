package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// catalogModel mirrors the object `mavor models list --json` emits. The
// harness reads the catalog through the shipped binary rather than importing
// it, for two reasons: modelCatalog lives in package main of cmd/mavor and is
// not importable, and going through the CLI means the benchmark measures the
// same catalog a user sees. A model added to the catalog is in scope here the
// moment it is added, with no second list to update — which is the whole
// point, given that the previous harness hardcoded three models and drifted.
type catalogModel struct {
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases"`
	Engine      string   `json:"engine"`
	Family      string   `json:"family"`
	Description string   `json:"description"`
	Languages   string   `json:"languages"`
	Streaming   bool     `json:"streaming"`
	Transducer  bool     `json:"transducer"`
	Speed       string   `json:"speed"`
	MeasuredRTF float64  `json:"measured_rtf"`

	Installed     bool  `json:"installed"`
	InstalledSize int64 `json:"installed_size"`
	DownloadSize  int64 `json:"download_size"`
	Active        bool  `json:"active"`
}

type catalog struct {
	ModelDir string         `json:"model_dir"`
	Models   []catalogModel `json:"models"`
}

// loadCatalog shells out to the mavor binary for the catalog.
func loadCatalog(mavorBin string) (*catalog, error) {
	out, err := exec.Command(mavorBin, "models", "list", "--json").Output()
	if err != nil {
		var stderr string
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		if stderr != "" {
			return nil, fmt.Errorf("%s models list --json: %w: %s", mavorBin, err, stderr)
		}
		return nil, fmt.Errorf("%s models list --json: %w", mavorBin, err)
	}
	var c catalog
	if err := json.Unmarshal(out, &c); err != nil {
		return nil, fmt.Errorf("parsing catalog from %s: %w", mavorBin, err)
	}
	if len(c.Models) == 0 {
		return nil, fmt.Errorf("%s reported an empty catalog", mavorBin)
	}
	return &c, nil
}

// selectModels narrows the catalog to what will actually be run: installed
// models only, optionally filtered by name. An uninstalled model is skipped
// rather than downloaded — a benchmark run should not quietly pull 16 GB, and
// the report says outright which models were absent so the gap is visible
// instead of looking like a model that scored nothing.
func selectModels(c *catalog, only []string) (selected, missing []catalogModel) {
	want := map[string]bool{}
	for _, n := range only {
		want[strings.TrimSpace(n)] = true
	}
	for _, m := range c.Models {
		if len(want) > 0 {
			matched := want[m.Name]
			for _, a := range m.Aliases {
				matched = matched || want[a]
			}
			if !matched {
				continue
			}
		}
		if m.Installed {
			selected = append(selected, m)
		} else {
			missing = append(missing, m)
		}
	}
	return selected, missing
}
