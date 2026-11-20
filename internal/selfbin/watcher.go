package selfbin

import "time"

// CheckInterval is how often a Watcher stats its executable. A stat is
// microseconds; the interval exists so the two-check confirmation spans a real
// gap rather than two stats of a file mid-write.
const CheckInterval = 10 * time.Second

// RestartFloor is the anti-loop guard. A re-exec'd process stamps at its own
// start, so a stamp that compares unequal for some reason other than a new
// binary would otherwise re-exec on every check.
const RestartFloor = 30 * time.Second

// Watcher notices that the path this process was launched from now holds a
// different binary. One implementation of the rules, shared by every client
// and the daemon, so the two cannot disagree about when an install happened.
type Watcher struct {
	Prober Prober

	started   time.Time
	atStart   Stamp
	onDisk    Stamp
	pending   Stamp
	lastCheck time.Time
}

func NewWatcher(p Prober, now time.Time) Watcher {
	stamp, _ := p.Current()
	return Watcher{Prober: p, started: now, atStart: stamp, onDisk: stamp}
}

// AtStart is the stamp of the image this process is running. Zero means the
// startup probe failed and no later one has succeeded.
func (w *Watcher) AtStart() Stamp { return w.atStart }

// OnDisk is the most recent stamp a check could get. Zero means none could.
func (w *Watcher) OnDisk() Stamp { return w.onDisk }

// Replaced reports whether the binary on disk has changed, confirmed, and this
// process has run long enough to act on it. It stats at most once per
// CheckInterval and reads a failed stat as unchanged: a process that cannot
// stat itself must never conclude it is out of date.
func (w *Watcher) Replaced(now time.Time) bool {
	if !w.lastCheck.IsZero() && now.Sub(w.lastCheck) < CheckInterval {
		return false
	}
	w.lastCheck = now

	stamp, ok := w.Prober.Current()
	if !ok {
		return false
	}
	w.onDisk = stamp
	// A startup probe that failed left atStart zero, which is "unknown" rather
	// than a real prior value: comparing against it would restart a binary
	// that never changed. The first stamp this process can actually get is its
	// baseline.
	if w.atStart.Zero() {
		w.atStart = stamp
	}
	if stamp == w.atStart {
		w.pending = Stamp{}
		return false
	}
	// The same changed stamp has to survive two checks a CheckInterval apart.
	// `make build` writes ./vigil in place, and a file still being written
	// grows between them, so a torn read is never what triggers the exec.
	if stamp != w.pending {
		w.pending = stamp
		return false
	}
	return now.Sub(w.started) >= RestartFloor
}
