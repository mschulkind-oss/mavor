package main

import (
	"strings"
	"testing"
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
