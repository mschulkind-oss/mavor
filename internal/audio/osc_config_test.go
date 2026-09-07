package audio

import (
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// internal/config restates these rather than importing this package, so that
// reading a config file does not pull in the audio stack. This is what stops
// the two copies drifting: a user who deleted the port line would otherwise
// get a different default from one who never had it.
func TestOSCConfigDefaultsMatchTheAudioConstants(t *testing.T) {
	if config.DefaultOSCPort != DefaultOSCPort {
		t.Errorf("config.DefaultOSCPort = %d, audio.DefaultOSCPort = %d",
			config.DefaultOSCPort, DefaultOSCPort)
	}
	if config.DefaultOSCTimeoutMS != DefaultOSCTimeoutMS {
		t.Errorf("config.DefaultOSCTimeoutMS = %d, audio.DefaultOSCTimeoutMS = %d",
			config.DefaultOSCTimeoutMS, DefaultOSCTimeoutMS)
	}

	d := config.Default()
	if d.Ducking.OSC.Port != DefaultOSCPort || d.Ducking.OSC.TimeoutMS != DefaultOSCTimeoutMS {
		t.Errorf("config.Default().Ducking.OSC = %+v, want port %d and timeout %d",
			d.Ducking.OSC, DefaultOSCPort, DefaultOSCTimeoutMS)
	}
	if d.Ducking.OSC.Enabled {
		t.Error("OSC ducking must be off by default: it addresses hardware most users do not have")
	}
}
