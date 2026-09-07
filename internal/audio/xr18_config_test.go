package audio

import (
	"testing"

	"github.com/mschulkind-oss/mavor/internal/config"
)

// internal/config restates these rather than importing this package, so that
// reading a config file does not pull in the audio stack. This is what stops
// the two copies drifting: a user who deleted the port line would otherwise
// get a different default from one who never had it.
func TestConfigDefaultsMatchTheAudioConstants(t *testing.T) {
	if config.DefaultXR18Port != DefaultXR18Port {
		t.Errorf("config.DefaultXR18Port = %d, audio.DefaultXR18Port = %d",
			config.DefaultXR18Port, DefaultXR18Port)
	}
	if config.DefaultXR18TimeoutMS != DefaultXR18TimeoutMS {
		t.Errorf("config.DefaultXR18TimeoutMS = %d, audio.DefaultXR18TimeoutMS = %d",
			config.DefaultXR18TimeoutMS, DefaultXR18TimeoutMS)
	}

	d := config.Default()
	if d.XR18.Port != DefaultXR18Port || d.XR18.TimeoutMS != DefaultXR18TimeoutMS {
		t.Errorf("config.Default().XR18 = %+v, want port %d and timeout %d",
			d.XR18, DefaultXR18Port, DefaultXR18TimeoutMS)
	}
	if d.XR18.Enabled {
		t.Error("XR18 ducking must be off by default: it addresses hardware most users do not have")
	}
}
