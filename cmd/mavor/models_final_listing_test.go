package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/models"
)

func TestFinalListingCapabilitiesAndSelection(t *testing.T) {
	cfg := config.Default()
	cfg.Paths.Models = t.TempDir()
	cfg.Model = "parakeet-tdt-0.6b-v2"
	dir := filepath.Join(cfg.Paths.Models, "sherpa", cfg.Model)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tokens.txt"), []byte("fixture"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, installedOnly := range []bool{false, true} {
		var b bytes.Buffer
		if err := listCatalogJSON(&b, cfg, installedOnly, models.FinalSegments); err != nil {
			t.Fatal(err)
		}
		var out catalogJSON
		if err := json.Unmarshal(b.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if out.ConfiguredFinalMode != models.FinalSegments {
			t.Fatal("missing configured mode")
		}
		wantLen := len(models.Catalog)
		if installedOnly {
			wantLen = 1
		}
		if len(out.Models) != wantLen {
			t.Fatalf("rows=%d want %d", len(out.Models), wantLen)
		}
		for _, row := range out.Models {
			m, _ := models.Lookup(row.Name)
			c := m.FinalCapabilities()
			if row.Streaming != m.Streaming || row.NativeStreaming != c.NativeStreaming || row.StreamingFinal != c.StreamingFinal || row.IncrementalSegments != c.IncrementalSegments || !reflect.DeepEqual(row.FinalModes, c.Modes()) {
				t.Errorf("bad capability row: %+v", row)
			}
			selected := models.FinalMode("")
			if row.Name == cfg.Model {
				selected = models.FinalSegments
			}
			if row.SelectedFinalMode != selected {
				t.Errorf("%s selected=%s", row.Name, row.SelectedFinalMode)
			}
			if row.Engine != m.Engine || row.DownloadS != m.DownloadSize || row.URL != m.URL || row.Transducer != m.Transducer || row.Filename != m.Filename {
				t.Errorf("legacy fields changed: %+v", row)
			}
		}
		for _, verbose := range []bool{false, true} {
			b.Reset()
			var err error
			if verbose {
				err = listCatalogVerbose(&b, cfg, installedOnly, models.FinalSegments)
			} else {
				err = listCatalog(&b, cfg, installedOnly, models.FinalSegments)
			}
			if err != nil {
				t.Fatal(err)
			}
			text := b.String()
			for _, row := range out.Models {
				if !strings.Contains(text, row.Name) {
					t.Errorf("missing %s", row.Name)
				}
				if verbose {
					block := strings.Split(strings.Split(text, "\n"+row.Name+"\n")[1], "\n\n")[0]
					for _, want := range []string{"native-stream", "final-modes", "selected", finalModesText(models.KnownModel{Name: row.Name, Engine: row.Engine, Streaming: row.Streaming}.FinalCapabilities())} {
						if !strings.Contains(block, want) {
							t.Errorf("%s missing %q: %s", row.Name, want, block)
						}
					}
				} else {
					var line string
					for _, s := range strings.Split(text, "\n") {
						if strings.HasPrefix(s, row.Name+" ") {
							line = s
							break
						}
					}
					fields := regexp.MustCompile(` {2,}`).Split(strings.TrimSpace(line), -1)
					if len(fields) < 9 {
						t.Fatalf("incomplete row %q", line)
					}
					native := "no"
					if row.NativeStreaming {
						native = "yes"
					}
					selected := "–"
					if row.Active {
						selected = "segments"
					}
					if fields[4] != native || fields[5] != finalModesText(models.KnownModel{Name: row.Name, Engine: row.Engine, Streaming: row.Streaming}.FinalCapabilities()) || fields[6] != selected {
						t.Errorf("capabilities/selection mismatch: %q", line)
					}
				}
			}
			if verbose {
				if !strings.Contains(text, "prototype") || !strings.Contains(text, "quality or speed guarantee") {
					t.Error("missing caveat")
				}
			} else {
				for _, h := range []string{"NATIVE-STREAM", "FINAL-MODES", "SELECTED"} {
					if !strings.Contains(text, h) {
						t.Errorf("missing %s", h)
					}
				}
			}
		}
	}
}

func TestFinalListingDefaultAndCustomPath(t *testing.T) {
	cfg := config.Default()
	cfg.Paths.Models = t.TempDir()
	for _, name := range []string{"nemotron-streaming-en-560ms", "/models/nemotron-streaming-en-560ms"} {
		cfg.Model = name
		var b bytes.Buffer
		if err := listCatalogJSON(&b, cfg, false); err != nil {
			t.Fatal(err)
		}
		var raw struct {
			Configured string                       `json:"configured_final_mode"`
			Models     []map[string]json.RawMessage `json:"models"`
		}
		if err := json.Unmarshal(b.Bytes(), &raw); err != nil {
			t.Fatal(err)
		}
		if raw.Configured != "after-stop" {
			t.Fatal("default isn't after-stop")
		}
		activeCount := 0
		for _, row := range raw.Models {
			var active bool
			_ = json.Unmarshal(row["active"], &active)
			selected, exists := row["selected_final_mode"]
			if active {
				activeCount++
				if string(selected) != `"after-stop"` {
					t.Error("native capability implied active live mode")
				}
			} else if exists {
				t.Error("nonactive selection not omitted")
			}
		}
		want := 1
		if filepath.IsAbs(name) {
			want = 0
		}
		if activeCount != want {
			t.Errorf("%s: active=%d", name, activeCount)
		}
	}
}

func TestFinalListingNativeSelectedModes(t *testing.T) {
	cfg := config.Default()
	cfg.Paths.Models = t.TempDir()
	cfg.Model = "nemotron-streaming-en-560ms"
	for _, mode := range []models.FinalMode{models.FinalAfterStop, models.FinalStreaming, models.FinalSegments} {
		// Even an unsupported configured selection is not relabeled as an
		// effective mode. Runtime validation, not a capability listing, rejects it.
		var b bytes.Buffer
		if err := listCatalogJSON(&b, cfg, false, mode); err != nil {
			t.Fatal(err)
		}
		var out catalogJSON
		if err := json.Unmarshal(b.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		for _, row := range out.Models {
			if row.Active && (row.SelectedFinalMode != mode || !row.NativeStreaming || row.IncrementalSegments) {
				t.Errorf("bad native selected row: %+v", row)
			}
		}
		b.Reset()
		if err := listCatalog(&b, cfg, false, mode); err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(b.String(), "\n") {
			if strings.HasPrefix(line, cfg.Model+" ") {
				columns := regexp.MustCompile(` {2,}`).Split(strings.TrimSpace(line), -1)
				if columns[6] != string(mode) {
					t.Errorf("selected native mode missing: %s", line)
				}
			}
		}
		b.Reset()
		if err := listCatalogVerbose(&b, cfg, false, mode); err != nil {
			t.Fatal(err)
		}
		block := strings.Split(strings.Split(b.String(), "\n"+cfg.Model+"\n")[1], "\n\n")[0]
		if !strings.Contains(block, string(mode)+" — configured") {
			t.Errorf("verbose selected mode missing: %s", block)
		}
	}
}

func TestFinalListingReadsConfiguredMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("model = \"parakeet-tdt-0.6b-v2\"\n[advanced]\nfinal_mode = \"segments\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := loaded.Config
	cfg.Paths.Models = t.TempDir()
	if got := configuredFinalMode(cfg); got != models.FinalSegments {
		t.Fatalf("configured mode = %s, want segments", got)
	}
	var b bytes.Buffer
	if err := listCatalogJSON(&b, cfg, false); err != nil {
		t.Fatal(err)
	}
	var out catalogJSON
	if err := json.Unmarshal(b.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.ConfiguredFinalMode != models.FinalSegments {
		t.Fatalf("JSON mode: %s", out.ConfiguredFinalMode)
	}
	b.Reset()
	if err := listCatalogVerbose(&b, cfg, false); err != nil {
		t.Fatal(err)
	}
	block := strings.Split(strings.Split(b.String(), "\n"+cfg.Model+"\n")[1], "\n\n")[0]
	if !strings.Contains(block, "segments — configured") {
		t.Fatalf("verbose config missing: %s", block)
	}
	b.Reset()
	if err := listCatalog(&b, cfg, false); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(b.String(), "\n") {
		if strings.HasPrefix(line, cfg.Model+" ") {
			columns := regexp.MustCompile(` {2,}`).Split(strings.TrimSpace(line), -1)
			if columns[6] != "segments" {
				t.Fatalf("plain config missing: %s", line)
			}
			return
		}
	}
	t.Fatal("active plain row missing")
}
