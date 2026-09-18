package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
	"github.com/mschulkind-oss/mavor/internal/speech"
)

func unitWith(execStart string) string {
	return `[Service]
ExecStart=` + execStart + `
Restart=on-failure
`
}

func TestExecStartBinary(t *testing.T) {
	tests := []struct {
		name string
		unit string
		want string
	}{
		{
			name: "the path installed units carry",
			unit: unitWith("/home/matt/.local/bin/mavor daemon"),
			want: "/home/matt/.local/bin/mavor",
		},
		{
			name: "flags after the binary are not part of it",
			unit: unitWith("/usr/bin/mavor daemon --verbose --log-file /tmp/m.log"),
			want: "/usr/bin/mavor",
		},
		{
			name: "systemd's modifier prefixes are not part of it",
			unit: unitWith("-/usr/bin/mavor daemon"),
			want: "/usr/bin/mavor",
		},
		{
			name: "no ExecStart at all",
			unit: "[Service]\nType=simple\n",
			want: "",
		},
		{
			name: "an ExecStart that names nothing",
			unit: "[Service]\nExecStart=\n",
			want: "",
		},
		{
			name: "the first ExecStart wins",
			unit: unitWith("/usr/bin/mavor daemon") + "ExecStart=/other/mavor daemon\n",
			want: "/usr/bin/mavor",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := execStartBinary(tc.unit); got != tc.want {
				t.Errorf("execStartBinary() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExecStartVerdict(t *testing.T) {
	const keg = "/home/linuxbrew/.linuxbrew/Cellar/mavor/0.1.1/bin/mavor"
	const opt = "/home/linuxbrew/.linuxbrew/opt/mavor/bin/mavor"
	const local = "/home/matt/.local/bin/mavor"

	all := func(string) bool { return true }
	none := func(string) bool { return false }

	tests := []struct {
		name    string
		unit    string
		want    string
		exists  func(string) bool
		wantOK  bool
		wantMsg string
	}{
		{
			name:   "the unit names what install would write today",
			unit:   unitWith(opt + " daemon"),
			want:   opt,
			exists: all,
			wantOK: true,
		},
		{
			// The whole reason this check exists: it still runs, and it
			// stops running the moment somebody runs `brew cleanup`.
			name:    "a keg path is stale even while it exists",
			unit:    unitWith(keg + " daemon"),
			want:    opt,
			exists:  all,
			wantOK:  false,
			wantMsg: "brew cleanup",
		},
		{
			name:    "a path that is already gone",
			unit:    unitWith(local + " daemon"),
			want:    opt,
			exists:  none,
			wantOK:  false,
			wantMsg: "no longer exists",
		},
		{
			name:    "no ExecStart line",
			unit:    "[Service]\nType=simple\n",
			want:    opt,
			exists:  all,
			wantOK:  false,
			wantMsg: "no ExecStart line",
		},
		{
			// Two installs on one machine is a choice, not a fault: the
			// service runs one of them and doctor was invoked from the other.
			name:    "a different but working binary is a note, not a failure",
			unit:    unitWith(local + " daemon"),
			want:    opt,
			exists:  all,
			wantOK:  true,
			wantMsg: local,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := execStartVerdict(tc.unit, tc.want, tc.exists)
			if ok != tc.wantOK {
				t.Errorf("execStartVerdict() ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			if tc.wantMsg == "" && msg != "" {
				t.Errorf("execStartVerdict() msg = %q, want empty", msg)
			}
			if tc.wantMsg != "" && !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("execStartVerdict() msg = %q, want it to mention %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestPreviewVerdict(t *testing.T) {
	tests := []struct {
		name    string
		plan    speech.PreviewPlan
		err     error
		wantOK  bool
		wantMsg []string
	}{
		{
			name: "a companion that is installed passes and names itself",
			plan: speech.PreviewPlan{
				Mode:      speech.PreviewCompanion,
				Companion: speech.DefaultCompanionModel,
				Reason:    "does not decode incrementally",
			},
			wantOK:  true,
			wantMsg: []string{"companion", speech.DefaultCompanionModel},
		},
		{
			// The point of this check: the daemon starts anyway, in a worse
			// mode than the config asked for, and doctor is where a user
			// finds that out — with the command that fixes it.
			name: "a missing companion fails and prompts for setup",
			plan: speech.PreviewPlan{
				Mode:    speech.PreviewPhrases,
				Reason:  "the companion is not installed",
				Missing: []string{speech.DefaultCompanionModel},
			},
			wantOK:  false,
			wantMsg: []string{"run 'mavor setup'", speech.DefaultCompanionModel},
		},
		{
			name: "phrase mode the user asked for is not a failure",
			plan: speech.PreviewPlan{
				Mode:   speech.PreviewPhrases,
				Reason: `preview.source = "phrases" asked for the main model at every pause`,
			},
			wantOK:  true,
			wantMsg: []string{"phrases"},
		},
		{
			name:    "a model named in the config and missing stays fatal",
			err:     errors.New(`speech: preview.source = "nonesuch": not installed`),
			wantOK:  false,
			wantMsg: []string{"nonesuch"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := previewVerdict(tc.plan, tc.err)
			if ok != tc.wantOK {
				t.Errorf("previewVerdict() ok = %v, want %v (msg %q)", ok, tc.wantOK, msg)
			}
			for _, want := range tc.wantMsg {
				if !strings.Contains(msg, want) {
					t.Errorf("previewVerdict() msg = %q, want it to mention %q", msg, want)
				}
			}
			if tc.wantOK && strings.Contains(msg, "mavor setup") {
				t.Errorf("previewVerdict() msg = %q, a passing check must not prompt for setup", msg)
			}
		})
	}
}

func TestOutputVerdict(t *testing.T) {
	defaultCfg := config.Default()
	pasteCfg := config.Default()
	pasteCfg.Output.Driver = "paste"
	pasteCfg.Output.PasteChord = "shift+insert"
	pasteCfg.Output.RestoreSelection = true

	tests := []struct {
		name               string
		cfg                config.Config
		wtype              bool
		wlCopy             bool
		wlPaste            bool
		pasteOnceSupported bool
		kittyBinding       string
		wantOK             bool
		wantMsg            string
	}{
		{
			name:    "typing mode with wtype present passes",
			cfg:     defaultCfg,
			wtype:   true,
			wantOK:  true,
			wantMsg: "typing driver",
		},
		{
			name:    "typing mode missing wtype fails",
			cfg:     defaultCfg,
			wtype:   false,
			wantOK:  false,
			wantMsg: "virtual typing requires wtype",
		},
		{
			name:               "paste mode all tools present passes",
			cfg:                pasteCfg,
			wtype:              true,
			wlCopy:             true,
			wlPaste:            true,
			pasteOnceSupported: true,
			kittyBinding:       "paste_from_selection",
			wantOK:             true,
			wantMsg:            "paste driver (shift+insert, dual-buffer, restore: true) — Kitty: paste_from_selection",
		},
		{
			name:               "paste mode missing wl-paste fails",
			cfg:                pasteCfg,
			wtype:              true,
			wlCopy:             true,
			wlPaste:            false,
			pasteOnceSupported: true,
			wantOK:             false,
			wantMsg:            "wl-clipboard tools missing",
		},
		{
			name:               "paste mode missing wtype fails",
			cfg:                pasteCfg,
			wtype:              false,
			wlCopy:             true,
			wlPaste:            true,
			pasteOnceSupported: true,
			wantOK:             false,
			wantMsg:            "paste driver requires wtype",
		},
		{
			name:               "paste mode wl-copy missing paste-once fails",
			cfg:                pasteCfg,
			wtype:              true,
			wlCopy:             true,
			wlPaste:            true,
			pasteOnceSupported: false,
			wantOK:             false,
			wantMsg:            "wl-copy does not support --paste-once",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ok, msg := outputVerdict(tc.cfg, tc.wtype, tc.wlCopy, tc.wlPaste, tc.pasteOnceSupported, tc.kittyBinding)
			if ok != tc.wantOK {
				t.Errorf("outputVerdict() ok = %v, want %v", ok, tc.wantOK)
			}
			if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("outputVerdict() msg = %q, want containing %q", msg, tc.wantMsg)
			}
		})
	}
}

func TestInspectKittyConfig(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "empty config returns default",
			content: "",
			want:    "default (paste_from_selection, covered by dual-buffer)",
		},
		{
			name:    "commented map shift+insert returns default",
			content: "# map shift+insert paste_from_clipboard\n",
			want:    "default (paste_from_selection, covered by dual-buffer)",
		},
		{
			name:    "explicit paste_from_clipboard mapped",
			content: "map shift+insert paste_from_clipboard\n",
			want:    "paste_from_clipboard",
		},
		{
			name:    "explicit paste_from_selection mapped with extra spaces",
			content: "  map   shift+insert   paste_from_selection  \n",
			want:    "paste_from_selection",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := inspectKittyConfig(tc.content)
			if got != tc.want {
				t.Errorf("inspectKittyConfig() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCheckPasteOnceSupport(t *testing.T) {
	if !checkPasteOnceSupport("Options:\n  -o, --paste-once  Only serve one paste\n") {
		t.Error("expected true when --paste-once is in help text")
	}
	if !checkPasteOnceSupport("Options:\n  -o  Only serve one paste\n") {
		t.Error("expected true when -o is in help text")
	}
	if checkPasteOnceSupport("Options:\n  -c, --clear  Clear clipboard\n") {
		t.Error("expected false when paste-once is absent")
	}
}
