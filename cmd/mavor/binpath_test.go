package main

import "testing"

func TestStableBinaryPath(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "linuxbrew keg becomes the opt path",
			in:   "/home/linuxbrew/.linuxbrew/Cellar/mavor/0.2.0/bin/mavor",
			want: "/home/linuxbrew/.linuxbrew/opt/mavor/bin/mavor",
		},
		{
			name: "any prefix works, the prefix is read off the path",
			in:   "/opt/homebrew/Cellar/mavor/0.2.0_1/bin/mavor",
			want: "/opt/homebrew/opt/mavor/bin/mavor",
		},
		{
			name: "a deeper tail is kept whole",
			in:   "/usr/local/Cellar/mavor/1.0.0/libexec/bin/mavor",
			want: "/usr/local/opt/mavor/libexec/bin/mavor",
		},
		{
			name: "a local install is left alone",
			in:   "/home/matt/.local/bin/mavor",
			want: "/home/matt/.local/bin/mavor",
		},
		{
			name: "a repo build is left alone",
			in:   "/home/matt/code/mavor/bin/mavor",
			want: "/home/matt/code/mavor/bin/mavor",
		},
		{
			// Nothing follows the version, so there is no keg layout here to
			// rewrite — a directory that merely happens to be called Cellar.
			name: "too few segments to be a keg",
			in:   "/home/matt/Cellar/notes/2026",
			want: "/home/matt/Cellar/notes/2026",
		},
		{
			name: "a Cellar with no version below it",
			in:   "/usr/local/Cellar/mavor",
			want: "/usr/local/Cellar/mavor",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := stableBinaryPath(tc.in); got != tc.want {
				t.Errorf("stableBinaryPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRestartOnUpgrade(t *testing.T) {
	tests := []struct {
		name         string
		invocationID string
		override     string
		want         bool
	}{
		{name: "off outside systemd", want: false},
		{name: "on under systemd", invocationID: "7c2b0e1f", want: true},
		{name: "override on", override: "1", want: true},
		{name: "override on by word", override: "TRUE", want: true},
		{name: "override off beats systemd", invocationID: "7c2b0e1f", override: "off", want: false},
		{name: "unrecognised override falls back", invocationID: "7c2b0e1f", override: "maybe", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOCATION_ID", tc.invocationID)
			t.Setenv("MAVOR_RESTART_ON_UPGRADE", tc.override)
			if got := restartOnUpgrade(); got != tc.want {
				t.Errorf("restartOnUpgrade() = %v, want %v", got, tc.want)
			}
		})
	}
}
