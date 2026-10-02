package config

import (
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// Check the public TOML contract, including serialization by config show.
func assertCPUFallback(t *testing.T, cfg Config, want bool) {
	t.Helper()
	body, err := toml.Marshal(cfg.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := toml.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if got, ok := fields["cpu_fallback"].(bool); !ok || got != want {
		t.Fatalf("cpu_fallback = %v, want %v (bool)", fields["cpu_fallback"], want)
	}
}

func TestCPUFallbackDefaultAndTOML(t *testing.T) {
	t.Run("compiled default", func(t *testing.T) { assertCPUFallback(t, Default(), false) })
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"omitted", "[advanced]\n", false},
		{"enabled", "[advanced]\ncpu_fallback = true\n", true},
		{"disabled", "[advanced]\ncpu_fallback = false\n", false},
		{"explicit CPU", "[advanced]\ngpu = \"off\"\n", false},
		{"explicit CPU with opt-in", "[advanced]\ngpu = \"off\"\ncpu_fallback = true\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file, err := LoadFile(writeConfig(t, tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(file.UnknownKeys) != 0 {
				t.Fatalf("unknown keys: %v", file.UnknownKeys)
			}
			assertCPUFallback(t, file.Config, tc.want)
			if tc.name == "explicit CPU" || tc.name == "explicit CPU with opt-in" {
				if !file.Config.GPUOff() {
					t.Fatal("gpu = off must retain explicit CPU operation")
				}
			}
		})
	}
}
