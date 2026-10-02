package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFinalModeDefaultResolveAndLoad(t *testing.T) {
	if Default().Advanced.FinalMode != "after-stop" {
		t.Fatal("default must remain after-stop")
	}
	c := Config{}
	c.Resolve()
	if c.Advanced.FinalMode != "after-stop" {
		t.Fatal("resolve default")
	}
	for _, mode := range []string{"after-stop", "streaming", "segments", "typo", "Streaming"} {
		t.Run(mode, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if e := os.WriteFile(p, []byte("[advanced]\nfinal_mode = \""+mode+"\"\n"), 0600); e != nil {
				t.Fatal(e)
			}
			f, e := LoadFile(p)
			valid := mode != "typo" && mode != "Streaming"
			if valid && (e != nil || f.Config.Advanced.FinalMode != mode) || !valid && e == nil {
				t.Fatalf("%+v %v", f, e)
			}
		})
	}
}
