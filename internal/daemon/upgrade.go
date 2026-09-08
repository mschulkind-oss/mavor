package daemon

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mschulkind-oss/mavor/internal/state"
)

// ErrBinaryReplaced is what Run returns when the file behind BinaryPath was
// replaced — a package upgrade — and the daemon stepped aside so its
// supervisor would start the new one. Nothing went wrong: the process was
// simply still running code that is no longer installed.
//
// `mavor daemon` turns it into a non-zero exit because that is what makes
// systemd's Restart=on-failure bring the new version up. See exitUpgraded in
// cmd/mavor.
var ErrBinaryReplaced = errors.New("binary replaced by a newer install; exiting so the supervisor starts it")

// defaultUpgradeInterval is how often the binary is stat'd. A replacement has
// to survive one further tick before it counts (see watchBinary), so this is
// half the worst-case delay between an install finishing and the new daemon
// running.
const defaultUpgradeInterval = 30 * time.Second

// binaryFingerprint identifies the *file* at a path, not its contents. Every
// way of installing mavor lands a new file rather than writing through the
// old one — Homebrew retargets the opt symlink at a fresh keg, `just install`
// copies over the path — so device and inode move even in the case where an
// archive restored a build-time mtime and the size happened to match.
type binaryFingerprint struct {
	dev     uint64
	ino     uint64
	size    int64
	modTime int64
}

func fingerprintBinary(path string) (binaryFingerprint, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return binaryFingerprint{}, err
	}
	fp := binaryFingerprint{size: fi.Size(), modTime: fi.ModTime().UnixNano()}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		fp.dev = uint64(st.Dev)
		fp.ino = st.Ino
	}
	return fp, nil
}

// watchBinary cancels the daemon once the binary underneath it changes, and
// records that it did so in replaced.
//
// Two conditions guard that, and both earn their keep. The new file has to
// still be there on the following tick, because an install is several file
// operations and coming back as the version that finished writing beats
// coming back as the one halfway through it. And the FSM has to be Idle: a
// restart mid-phrase throws away audio the user already spoke, and an upgrade
// can always wait for them to stop talking.
//
// The path this watches must be one that outlives the upgrade — the daemon's
// own /proc/self/exe is precisely the file that gets deleted. cmd/mavor
// passes the same path it writes into ExecStart.
func (d *Daemon) watchBinary(ctx context.Context, replaced *atomic.Bool, stop context.CancelFunc) {
	original, err := fingerprintBinary(d.binaryPath)
	if err != nil {
		d.logger.Warn("upgrade watch: cannot stat binary, not watching", "path", d.binaryPath, "err", err)
		return
	}
	d.logger.Info("upgrade watch: started", "path", d.binaryPath, "interval", d.upgradeInterval)

	ticker := time.NewTicker(d.upgradeInterval)
	defer ticker.Stop()

	var pending *binaryFingerprint
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		current, err := fingerprintBinary(d.binaryPath)
		if err != nil {
			// Mid-install the path is briefly absent. Wait for it to come
			// back rather than treating a gap as a new version.
			pending = nil
			continue
		}
		if current == original {
			pending = nil
			continue
		}
		if pending == nil || *pending != current {
			settled := current
			pending = &settled
			continue
		}
		if s := d.machine.State(); s != state.Idle {
			d.logger.Info("upgrade watch: binary replaced, waiting for idle", "state", s.String())
			continue
		}

		d.logger.Info("upgrade watch: binary replaced, exiting for restart", "path", d.binaryPath)
		replaced.Store(true)
		stop()
		return
	}
}
