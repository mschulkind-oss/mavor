package daemon

import (
	"context"
	"time"

	"github.com/mschulkind-oss/mavor/internal/speech"
)

// perfInterval is how often the machine's own counters are written to the log
// while verbose logging is on.
//
// It is a log record, not a feature: a dictation that feels slow is a question
// about minutes, and the bracketing readings at start/stop/done carry the
// seconds. A finer tick would bury those in a page of idle samples, and the
// overlay — which logs on a 30 ms tick when verbose — is already the loud part
// of a debug log.
const perfInterval = 10 * time.Second

// watchPerf writes one machine reading per tick for as long as the daemon runs.
// It exists only when verbose logging is on, so a normal run reads nothing.
func (d *Daemon) watchPerf(ctx context.Context) {
	ticker := time.NewTicker(d.perfInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.logPerf("periodic", 0)
		}
	}
}

// logPerf writes one line of machine counters at debug level: the host's load
// and memory, the card's utilization and its memory, and where the model's
// weights currently sit.
//
// The three facts are deliberately on one line, because the question they
// answer is not "how busy was the GPU" but "was this dictation slow because the
// machine was busy, because the card was busy, or because the model had been
// pushed out of it" — and only the reading taken at the same moment can
// separate those. The card's counters (vram_used, gtt_used) are every process's;
// model_vram and model_gtt are the model's own, from the residency probe, which
// is the only source that attributes memory to this process.
//
// phase says why the reading was taken: "start", "stop" and "done" bracket one
// dictation and name its cycle, "periodic" is the timer. Nothing here returns or
// blocks: at worst a counter reads zero.
func (d *Daemon) logPerf(phase string, cycleID uint64, extra ...any) {
	if d.perf == nil {
		return
	}
	d.residencyMu.Lock()
	device, res, mem := d.residencyMem.Device, d.residency, d.residencyMem
	d.residencyMu.Unlock()

	s := d.perf.Read(device)
	attrs := []any{
		"phase", phase,
		"state", d.machine.State().String(),
		"load1", s.Load1,
		"load5", s.Load5,
		"load15", s.Load15,
		"runnable", s.Runnable,
		"cpu_pct", s.CPUPercent,
		"mem_avail_mb", mib(s.MemAvailable),
		"swap_free_mb", mib(s.SwapFree),
	}
	if s.GPU.Present() {
		attrs = append(attrs,
			"gpu", s.GPU.Device,
			"gpu_busy_pct", s.GPU.BusyPct,
			"gpu_clock_mhz", s.GPU.ClockMHz,
			"vram_used_mb", mib(s.GPU.VRAMUsed),
			"vram_total_mb", mib(s.GPU.VRAMTotal),
			"gtt_used_mb", mib(s.GPU.GTTUsed),
			"gtt_total_mb", mib(s.GPU.GTTTotal),
		)
	} else {
		// An absent card is a fact worth writing: without it every counter
		// below would read as zero, which is also what a busy card at rest
		// reads as.
		attrs = append(attrs, "gpu", "none")
	}
	if res != speech.ResidencyUnknown {
		attrs = append(attrs,
			"model_residency", res.String(),
			"model_vram_mb", mib(mem.VRAM),
			"model_gtt_mb", mib(mem.GTT),
			"model_evicted_mb", mib(mem.Evicted),
		)
	}
	if cycleID != 0 {
		attrs = append(attrs, "cycle", cycleID)
	}
	attrs = append(attrs, extra...)
	d.logger.Debug("perf: machine", attrs...)
}

// mib is bytes as whole mebibytes: the unit /proc and sysfs counters are read
// in by a human, and the one the model's own footprint is compared against.
func mib(n uint64) uint64 { return n >> 20 }
