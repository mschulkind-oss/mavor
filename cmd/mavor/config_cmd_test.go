package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// The scaffolded file and the compiled defaults used to be two literals, and
// they drifted: `config init` wrote mode = "batch" where Default() said
// "streaming", duck_audio = true where Default() said false, and a top margin
// of 32 where Default() said 8. A user who ran `config init` silently got
// different behavior from one who did not.
//
// This is the test that makes that impossible. The template is generated from
// Default(), and parsing it must give Default() back exactly.
func TestScaffoldedTemplateParsesToTheCompiledDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(defaultConfigTemplate()), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("the scaffolded template does not parse: %v\n\n%s", err, defaultConfigTemplate())
	}
	if want := config.Default(); !reflect.DeepEqual(got, want) {
		t.Errorf("the scaffolded template and config.Default() disagree:\n got %+v\nwant %+v\n\ntemplate:\n%s",
			got, want, defaultConfigTemplate())
	}
}

// Every key the template writes must be one the loader knows. A commented-out
// example with a stale name would be advice that does not work, and the
// loader would only warn about it once a user uncommented it.
func TestScaffoldedTemplateUsesNoUnknownKeys(t *testing.T) {
	body := defaultConfigTemplate()
	// Uncomment every commented-out setting, so the examples are checked too.
	var uncommented []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "#"))
		if strings.HasPrefix(trimmed, "#") && strings.Contains(rest, " = ") && !strings.HasPrefix(rest, "#") {
			// "server = "http://…"" is a placeholder, not a URL to parse.
			if strings.Contains(rest, "…") {
				continue
			}
			uncommented = append(uncommented, rest)
			continue
		}
		uncommented = append(uncommented, line)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(strings.Join(uncommented, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := config.LoadFile(path)
	if err != nil {
		t.Fatalf("the template with its examples uncommented does not parse: %v\n\n%s", err, strings.Join(uncommented, "\n"))
	}
	if len(f.UnknownKeys) > 0 {
		t.Errorf("the template documents keys the loader does not know: %v", f.UnknownKeys)
	}
}

// Uncommenting a documented example must not change behavior either: each one
// states the default beside it, so a reader can uncomment a line to see the
// value without altering what mavor does.
func TestTemplateExamplesStateTheDefaults(t *testing.T) {
	body := defaultConfigTemplate()
	d := config.Default()

	for _, want := range []string{
		"pause_ms = 450",
		"min_phrase_ms = 600",
		"boost = 1.5",
		`placement = "auto"`,
		`gpu = "auto"`,
		"models = " + quote(d.Paths.Models),
		"log    = " + quote(d.Paths.Log),
		"socket = " + quote(d.Paths.Socket),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the template does not document %s\n\n%s", want, body)
		}
	}
}

func quote(s string) string { return `"` + s + `"` }
