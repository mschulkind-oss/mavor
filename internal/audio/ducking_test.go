package audio

import (
	"errors"
	"testing"
)

func TestMockDucker(t *testing.T) {
	m := &MockDucker{}
	if m.IsDucked() {
		t.Errorf("IsDucked = true, want false initially")
	}

	if err := m.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if !m.IsDucked() {
		t.Errorf("IsDucked = false, want true after Duck")
	}

	if err := m.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if m.IsDucked() {
		t.Errorf("IsDucked = true, want false after Restore")
	}

	duckCalls, restoreCalls := m.Calls()
	if duckCalls != 1 || restoreCalls != 1 {
		t.Errorf("Calls() = (%d, %d), want (1, 1)", duckCalls, restoreCalls)
	}

	// Error injection
	errBoom := errors.New("duck error")
	m.DuckErr = errBoom
	if err := m.Duck(); !errors.Is(err, errBoom) {
		t.Errorf("Duck() err = %v, want %v", err, errBoom)
	}

	errRestore := errors.New("restore error")
	m.RestoreErr = errRestore
	if err := m.Restore(); !errors.Is(err, errRestore) {
		t.Errorf("Restore() err = %v, want %v", err, errRestore)
	}
}

func TestNoopDucker(t *testing.T) {
	n := &NoopDucker{}
	if err := n.Duck(); err != nil {
		t.Errorf("NoopDucker.Duck() = %v", err)
	}
	if err := n.Restore(); err != nil {
		t.Errorf("NoopDucker.Restore() = %v", err)
	}
}

func TestCommandDuckerWpctl(t *testing.T) {
	type cmdLog struct {
		name string
		args []string
	}
	var executed []cmdLog

	ducker := NewWpctlDucker("0.2")
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		executed = append(executed, cmdLog{name: name, args: args})
		if name == "wpctl" && len(args) >= 2 && args[0] == "get-volume" {
			return []byte("Volume: 0.85\n"), nil
		}
		if name == "wpctl" && len(args) >= 3 && args[0] == "set-volume" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if !ducker.IsDucked() {
		t.Errorf("IsDucked = false, want true")
	}

	// Verify wpctl get-volume and set-volume 0.2 were executed
	if len(executed) != 2 {
		t.Fatalf("executed %d commands, want 2: %v", len(executed), executed)
	}
	if executed[0].name != "wpctl" || executed[0].args[0] != "get-volume" {
		t.Errorf("cmd 0 = %v, want wpctl get-volume", executed[0])
	}
	if executed[1].name != "wpctl" || executed[1].args[0] != "set-volume" || executed[1].args[2] != "0.2" {
		t.Errorf("cmd 1 = %v, want wpctl set-volume ... 0.2", executed[1])
	}

	// Second Duck should be idempotent
	if err := ducker.Duck(); err != nil {
		t.Fatalf("second Duck: %v", err)
	}
	if len(executed) != 2 {
		t.Errorf("second Duck should not have executed more commands, got %d", len(executed))
	}

	// Restore should restore original volume (0.85)
	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if ducker.IsDucked() {
		t.Errorf("IsDucked = true, want false after Restore")
	}
	if len(executed) != 3 {
		t.Fatalf("executed %d commands after restore, want 3", len(executed))
	}
	if executed[2].args[0] != "set-volume" || executed[2].args[2] != "0.85" {
		t.Errorf("restore cmd = %v, want wpctl set-volume ... 0.85", executed[2])
	}

	// Second Restore should be a safe no-op
	if err := ducker.Restore(); err != nil {
		t.Fatalf("second Restore: %v", err)
	}
	if len(executed) != 3 {
		t.Errorf("second Restore should not execute command, got %d", len(executed))
	}
}

func TestCommandDuckerPactl(t *testing.T) {
	type cmdLog struct {
		name string
		args []string
	}
	var executed []cmdLog

	ducker := NewPactlDucker("20%")
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		executed = append(executed, cmdLog{name: name, args: args})
		if name == "pactl" && len(args) >= 2 && args[0] == "get-sink-volume" {
			return []byte("Volume: front-left: 52428 /  80% / -5.81 dB,   front-right: 52428 /  80% / -5.81 dB\n"), nil
		}
		if name == "pactl" && len(args) >= 3 && args[0] == "set-sink-volume" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if !ducker.IsDucked() {
		t.Errorf("IsDucked = false, want true")
	}

	if len(executed) != 2 {
		t.Fatalf("executed %d commands, want 2: %v", len(executed), executed)
	}
	if executed[0].name != "pactl" || executed[0].args[0] != "get-sink-volume" {
		t.Errorf("cmd 0 = %v, want pactl get-sink-volume", executed[0])
	}
	if executed[1].name != "pactl" || executed[1].args[0] != "set-sink-volume" || executed[1].args[2] != "20%" {
		t.Errorf("cmd 1 = %v, want pactl set-sink-volume ... 20%%", executed[1])
	}

	// Restore should restore original volume (80%)
	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if ducker.IsDucked() {
		t.Errorf("IsDucked = true, want false after Restore")
	}
	if len(executed) != 3 {
		t.Fatalf("executed %d commands after restore, want 3", len(executed))
	}
	if executed[2].args[0] != "set-sink-volume" || executed[2].args[2] != "80%" {
		t.Errorf("restore cmd = %v, want pactl set-sink-volume ... 80%%", executed[2])
	}
}

func TestCommandDuckerErrors(t *testing.T) {
	// 1. Get volume failure
	ducker := NewWpctlDucker("0.2")
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		return []byte("error: device not found"), errors.New("exit status 1")
	})
	if err := ducker.Duck(); err == nil {
		t.Errorf("expected error on failed get-volume, got nil")
	}

	// 2. Set volume failure
	ducker2 := NewWpctlDucker("0.2")
	ducker2.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "get-volume" {
			return []byte("Volume: 0.90\n"), nil
		}
		return []byte("error: cannot set volume"), errors.New("exit status 1")
	})
	if err := ducker2.Duck(); err == nil {
		t.Errorf("expected error on failed set-volume, got nil")
	}

	// 3. Restore failure
	ducker3 := NewWpctlDucker("0.2")
	calls := 0
	ducker3.SetRunner(func(name string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte("Volume: 0.90\n"), nil
		}
		if calls == 2 {
			return []byte(""), nil // duck set-volume success
		}
		return []byte("error: cannot restore volume"), errors.New("exit status 1")
	})
	if err := ducker3.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if err := ducker3.Restore(); err == nil {
		t.Errorf("expected error on failed restore, got nil")
	}
}

func TestParseVolumeOutputs(t *testing.T) {
	casesWpctl := []struct {
		input string
		want  string
	}{
		{"Volume: 0.85\n", "0.85"},
		{"Volume: 0.50 [MUTED]\n", "0.50"},
		{"Volume: 1.00\n", "1.00"},
		{"Volume: 0.05 [MUTED]\n", "0.05"},
	}
	for _, tc := range casesWpctl {
		got, err := parseWpctlVolume(tc.input)
		if err != nil {
			t.Errorf("parseWpctlVolume(%q) error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("parseWpctlVolume(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}

	casesPactl := []struct {
		input string
		want  string
	}{
		{"Volume: front-left: 65536 / 100% / 0.00 dB,   front-right: 65536 / 100% / 0.00 dB\n", "100%"},
		{"Volume: mono: 32768 / 50% / -6.02 dB\n", "50%"},
		{"Volume: 0: 45% 1: 45%\n", "45%"},
	}
	for _, tc := range casesPactl {
		got, err := parsePactlVolume(tc.input)
		if err != nil {
			t.Errorf("parsePactlVolume(%q) error: %v", tc.input, err)
		}
		if got != tc.want {
			t.Errorf("parsePactlVolume(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}

	// Invalid outputs
	if _, err := parseWpctlVolume("garbage output"); err == nil {
		t.Errorf("parseWpctlVolume(garbage) should error")
	}
	if _, err := parsePactlVolume("garbage output without percents"); err == nil {
		t.Errorf("parsePactlVolume(garbage) should error")
	}
}

func TestCommandDuckerCustomSink_Wpctl(t *testing.T) {
	type cmdLog struct {
		name string
		args []string
	}
	var executed []cmdLog

	ducker := NewCommandDucker(BackendWpctl, "0.2", "42", nil)
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		executed = append(executed, cmdLog{name: name, args: args})
		if name == "wpctl" && len(args) >= 2 && args[0] == "get-volume" && args[1] == "42" {
			return []byte("Volume: 0.85\n"), nil
		}
		if name == "wpctl" && len(args) >= 3 && args[0] == "set-volume" && args[1] == "42" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("executed %d commands, want 2: %v", len(executed), executed)
	}
	if executed[0].args[1] != "42" {
		t.Errorf("get-volume sink = %q, want 42", executed[0].args[1])
	}
	if executed[1].args[1] != "42" || executed[1].args[2] != "0.2" {
		t.Errorf("set-volume sink = %v, want 42 0.2", executed[1].args)
	}

	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("executed %d commands, want 3: %v", len(executed), executed)
	}
	if executed[2].args[1] != "42" || executed[2].args[2] != "0.85" {
		t.Errorf("restore set-volume = %v, want 42 0.85", executed[2].args)
	}
}

func TestCommandDuckerCustomSink_Pactl(t *testing.T) {
	type cmdLog struct {
		name string
		args []string
	}
	var executed []cmdLog

	customSink := "alsa_output.pci-0000_00_1f.3.analog-stereo"
	ducker := NewCommandDucker(BackendPactl, "15%", customSink, nil)
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		executed = append(executed, cmdLog{name: name, args: args})
		if name == "pactl" && len(args) >= 2 && args[0] == "get-sink-volume" && args[1] == customSink {
			return []byte("Volume: front-left: 52428 /  80% / -5.81 dB,   front-right: 52428 /  80% / -5.81 dB\n"), nil
		}
		if name == "pactl" && len(args) >= 3 && args[0] == "set-sink-volume" && args[1] == customSink {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if len(executed) != 2 {
		t.Fatalf("executed %d commands, want 2: %v", len(executed), executed)
	}
	if executed[0].args[1] != customSink {
		t.Errorf("get-sink-volume sink = %q, want %q", executed[0].args[1], customSink)
	}
	if executed[1].args[1] != customSink || executed[1].args[2] != "15%" {
		t.Errorf("set-sink-volume args = %v, want [%s, 15%%]", executed[1].args, customSink)
	}

	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(executed) != 3 {
		t.Fatalf("executed %d commands, want 3: %v", len(executed), executed)
	}
	if executed[2].args[1] != customSink || executed[2].args[2] != "80%" {
		t.Errorf("restore set-sink-volume = %v, want [%s, 80%%]", executed[2].args, customSink)
	}
}

const mockPactlListSinkInputs = `Sink Input #1
	Driver: PipeWire
	Owner Module: n/a
	Client: 65
	Sink: 48
	Sample Specification: float32le 2ch 48000Hz
	Channel Map: front-left,front-right
	Format: pcm, format.sample_format = "\"float32le\""  format.rate = "48000"  format.channels = "2"  format.channel_map = "\"front-left,front-right\""
	Corked: no
	Mute: no
	Volume: front-left: 65536 / 100% / 0.00 dB,   front-right: 65536 / 100% / 0.00 dB
	        balance 0.00
	Buffer Latency: 0 usec
	Sink Latency: 0 usec
	Resample method: PipeWire
	Properties:
		application.name = "Spotify"
		media.name = "Spotify"
		node.name = "spotify"
		application.process.binary = "spotify"
		application.language = "en_US.UTF-8"
		window.x11.display = ":0"
		application.process.id = "1234"

Sink Input #2
	Driver: PipeWire
	Owner Module: n/a
	Client: 72
	Sink: 48
	Sample Specification: float32le 2ch 48000Hz
	Channel Map: front-left,front-right
	Format: pcm, format.sample_format = "\"float32le\""  format.rate = "48000"  format.channels = "2"  format.channel_map = "\"front-left,front-right\""
	Corked: no
	Mute: no
	Volume: front-left: 52428 /  80% / -5.81 dB,   front-right: 52428 /  80% / -5.81 dB
	        balance 0.00
	Buffer Latency: 0 usec
	Sink Latency: 0 usec
	Resample method: PipeWire
	Properties:
		application.name = "Firefox"
		media.name = "AudioStream"
		application.process.binary = "firefox"

Sink Input #3
	Driver: PipeWire
	Owner Module: n/a
	Client: 80
	Sink: 48
	Sample Specification: float32le 2ch 48000Hz
	Channel Map: front-left,front-right
	Format: pcm, format.sample_format = "\"float32le\""  format.rate = "48000"  format.channels = "2"  format.channel_map = "\"front-left,front-right\""
	Corked: no
	Mute: no
	Volume: front-left: 58982 /  90% / -1.82 dB,   front-right: 58982 /  90% / -1.82 dB
	        balance 0.00
	Buffer Latency: 0 usec
	Sink Latency: 0 usec
	Resample method: PipeWire
	Properties:
		application.name = "Discord"
		media.name = "WEBRTC VoiceEngine"
		application.process.binary = "Discord"
`

func TestCommandDuckerStreamSpecific_Pactl(t *testing.T) {
	type cmdLog struct {
		name string
		args []string
	}
	var executed []cmdLog

	ducker := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify", "firefox"})
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		executed = append(executed, cmdLog{name: name, args: args})
		if name == "pactl" && len(args) == 2 && args[0] == "list" && args[1] == "sink-inputs" {
			return []byte(mockPactlListSinkInputs), nil
		}
		if name == "pactl" && len(args) == 3 && args[0] == "set-sink-input-volume" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command: " + name)
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if !ducker.IsDucked() {
		t.Errorf("IsDucked = false, want true")
	}

	// Should list sink-inputs, then set volume on stream 1 (spotify) and stream 2 (firefox)
	if len(executed) != 3 {
		t.Fatalf("executed %d commands, want 3: %v", len(executed), executed)
	}
	if executed[0].args[0] != "list" || executed[0].args[1] != "sink-inputs" {
		t.Errorf("cmd 0 = %v, want pactl list sink-inputs", executed[0])
	}
	if executed[1].args[0] != "set-sink-input-volume" || executed[1].args[1] != "1" || executed[1].args[2] != "20%" {
		t.Errorf("cmd 1 = %v, want pactl set-sink-input-volume 1 20%%", executed[1])
	}
	if executed[2].args[0] != "set-sink-input-volume" || executed[2].args[1] != "2" || executed[2].args[2] != "20%" {
		t.Errorf("cmd 2 = %v, want pactl set-sink-input-volume 2 20%%", executed[2])
	}

	// Restore should restore stream 1 (100%) and stream 2 (80%) in order
	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if ducker.IsDucked() {
		t.Errorf("IsDucked = true, want false after Restore")
	}

	if len(executed) != 5 {
		t.Fatalf("executed %d commands after restore, want 5: %v", len(executed), executed)
	}
	if executed[3].args[0] != "set-sink-input-volume" || executed[3].args[1] != "1" || executed[3].args[2] != "100%" {
		t.Errorf("cmd 3 = %v, want pactl set-sink-input-volume 1 100%%", executed[3])
	}
	if executed[4].args[0] != "set-sink-input-volume" || executed[4].args[1] != "2" || executed[4].args[2] != "80%" {
		t.Errorf("cmd 4 = %v, want pactl set-sink-input-volume 2 80%%", executed[4])
	}

	// Second restore is a no-op
	if err := ducker.Restore(); err != nil {
		t.Fatalf("second Restore: %v", err)
	}
	if len(executed) != 5 {
		t.Errorf("second Restore executed unexpected commands, count = %d", len(executed))
	}
}

func TestCommandDuckerStreamSpecific_MediaNameMatch(t *testing.T) {
	mockVLC := `Sink Input #42
	Driver: PipeWire
	Volume: mono: 32768 / 50% / -6.02 dB
	Properties:
		application.name = "ALSA plug-in"
		media.name = "VLC media playback"
		application.process.binary = "vlc"
`
	var setVolumeArgs []string
	ducker := NewCommandDucker(BackendPactl, "20%", "", []string{"vlc"})
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(mockVLC), nil
		}
		if args[0] == "set-sink-input-volume" {
			setVolumeArgs = args
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if len(setVolumeArgs) != 3 || setVolumeArgs[1] != "42" || setVolumeArgs[2] != "20%" {
		t.Errorf("setVolumeArgs = %v, want [set-sink-input-volume 42 20%%]", setVolumeArgs)
	}

	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if len(setVolumeArgs) != 3 || setVolumeArgs[1] != "42" || setVolumeArgs[2] != "50%" {
		t.Errorf("restore setVolumeArgs = %v, want [set-sink-input-volume 42 50%%]", setVolumeArgs)
	}
}

func TestCommandDuckerStreamSpecific_CaseInsensitive(t *testing.T) {
	mockSpotify := `Sink Input #10
	Volume: front-left: 65536 / 100% / 0.00 dB
	Properties:
		application.name = "Spotify Premium"
`
	called := false
	ducker := NewCommandDucker(BackendPactl, "20%", "", []string{"SPOTIFY"})
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(mockSpotify), nil
		}
		if args[0] == "set-sink-input-volume" && args[1] == "10" {
			called = true
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if !called {
		t.Errorf("expected Spotify to be matched case-insensitively")
	}
}

func TestCommandDuckerStreamSpecific_NoMatchingStreams(t *testing.T) {
	mockOther := `Sink Input #99
	Volume: front-left: 65536 / 100% / 0.00 dB
	Properties:
		application.name = "Discord"
		media.name = "Voice"
`
	setCalls := 0
	ducker := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify"})
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(mockOther), nil
		}
		if args[0] == "set-sink-input-volume" {
			setCalls++
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if setCalls != 0 {
		t.Errorf("setCalls = %d, want 0 when no streams match", setCalls)
	}

	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if setCalls != 0 {
		t.Errorf("setCalls = %d, want 0 after restore", setCalls)
	}
}

func TestCommandDuckerStreamSpecific_EmptyOutput(t *testing.T) {
	ducker := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify"})
	ducker.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(""), nil
		}
		return nil, errors.New("unexpected command")
	})

	if err := ducker.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if err := ducker.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
}

func TestCommandDuckerStreamSpecific_Errors(t *testing.T) {
	// 1. List error
	ducker1 := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify"})
	ducker1.SetRunner(func(name string, args ...string) ([]byte, error) {
		return []byte("Connection refused"), errors.New("exit status 1")
	})
	if err := ducker1.Duck(); err == nil {
		t.Fatalf("expected error on list failure")
	}

	// 2. Set volume error during Duck -> should rollback previously ducked stream
	mockTwo := `Sink Input #1
	Volume: 100%
	Properties:
		application.name = "Spotify"
Sink Input #2
	Volume: 80%
	Properties:
		application.name = "Firefox"
`
	reverted := false
	ducker2 := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify", "firefox"})
	ducker2.SetRunner(func(name string, args ...string) ([]byte, error) {
		if args[0] == "list" {
			return []byte(mockTwo), nil
		}
		if args[0] == "set-sink-input-volume" && args[1] == "1" && args[2] == "20%" {
			return []byte(""), nil // stream 1 succeeds
		}
		if args[0] == "set-sink-input-volume" && args[1] == "2" && args[2] == "20%" {
			return []byte("cannot set volume"), errors.New("failed") // stream 2 fails
		}
		if args[0] == "set-sink-input-volume" && args[1] == "1" && args[2] == "100%" {
			reverted = true
			return []byte(""), nil // stream 1 reverted
		}
		return nil, errors.New("unexpected command: " + args[0])
	})

	if err := ducker2.Duck(); err == nil {
		t.Fatalf("expected error on stream 2 set-volume failure")
	}
	if !reverted {
		t.Errorf("expected stream 1 to be reverted after stream 2 failure")
	}

	// 3. Restore error
	ducker3 := NewCommandDucker(BackendPactl, "20%", "", []string{"spotify"})
	calls := 0
	ducker3.SetRunner(func(name string, args ...string) ([]byte, error) {
		calls++
		if calls == 1 {
			return []byte(mockPactlListSinkInputs), nil
		}
		if calls == 2 {
			return []byte(""), nil // duck stream 1 ok
		}
		return []byte("stream gone"), errors.New("exit status 1") // restore error
	})
	if err := ducker3.Duck(); err != nil {
		t.Fatalf("Duck: %v", err)
	}
	if err := ducker3.Restore(); err == nil {
		t.Fatalf("expected error on restore failure")
	}
}

func TestParsePactlSinkInputs(t *testing.T) {
	inputs := parsePactlSinkInputs(mockPactlListSinkInputs)
	if len(inputs) != 3 {
		t.Fatalf("parsed %d sink inputs, want 3", len(inputs))
	}

	// Check input 1
	if inputs[0].id != "1" {
		t.Errorf("input 0 id = %q, want 1", inputs[0].id)
	}
	if inputs[0].volume != "100%" {
		t.Errorf("input 0 volume = %q, want 100%%", inputs[0].volume)
	}
	if inputs[0].sink != "48" {
		t.Errorf("input 0 sink = %q, want 48", inputs[0].sink)
	}
	if inputs[0].properties["application.name"] != "Spotify" {
		t.Errorf("input 0 app.name = %q, want Spotify", inputs[0].properties["application.name"])
	}
	if inputs[0].properties["media.name"] != "Spotify" {
		t.Errorf("input 0 media.name = %q, want Spotify", inputs[0].properties["media.name"])
	}

	// Check input 2
	if inputs[1].id != "2" {
		t.Errorf("input 1 id = %q, want 2", inputs[1].id)
	}
	if inputs[1].volume != "80%" {
		t.Errorf("input 1 volume = %q, want 80%%", inputs[1].volume)
	}
	if inputs[1].properties["application.name"] != "Firefox" {
		t.Errorf("input 1 app.name = %q, want Firefox", inputs[1].properties["application.name"])
	}
	if inputs[1].properties["media.name"] != "AudioStream" {
		t.Errorf("input 1 media.name = %q, want AudioStream", inputs[1].properties["media.name"])
	}

	// Check input 3
	if inputs[2].id != "3" {
		t.Errorf("input 2 id = %q, want 3", inputs[2].id)
	}
	if inputs[2].volume != "90%" {
		t.Errorf("input 2 volume = %q, want 90%%", inputs[2].volume)
	}
	if inputs[2].properties["application.name"] != "Discord" {
		t.Errorf("input 2 app.name = %q, want Discord", inputs[2].properties["application.name"])
	}
}

func TestSetSinkAndStreamsMethods(t *testing.T) {
	d := NewCommandDucker(BackendPactl, "20%", "", nil)
	d.SetSink("alsa_custom")
	d.SetStreams([]string{"spotify"})

	if d.duckSink != "alsa_custom" {
		t.Errorf("duckSink = %q, want alsa_custom", d.duckSink)
	}
	if len(d.duckStreams) != 1 || d.duckStreams[0] != "spotify" {
		t.Errorf("duckStreams = %v, want [spotify]", d.duckStreams)
	}
}

// The default duck level is silence. Callers that want a partial reduction set
// duck_volume explicitly; an unset value must not quietly become 20%.
func TestCommandDuckerDefaultsToMute(t *testing.T) {
	for _, tc := range []struct {
		backend Backend
		want    string
	}{
		{BackendWpctl, "0"},
		{BackendPactl, "0%"},
	} {
		d := NewCommandDucker(tc.backend, "", "", nil)
		if d.duckVolume != tc.want {
			t.Errorf("%s default duckVolume = %q, want %q", tc.backend, d.duckVolume, tc.want)
		}
	}
}

// An explicit partial level is still honoured — muting is the default, not the
// only option.
func TestCommandDuckerHonoursExplicitVolume(t *testing.T) {
	d := NewCommandDucker(BackendPactl, "35%", "", nil)
	if d.duckVolume != "35%" {
		t.Errorf("duckVolume = %q, want the configured 35%%", d.duckVolume)
	}
}
