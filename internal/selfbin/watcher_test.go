package selfbin

import (
	"errors"
	"testing"
	"time"
)

var epoch = time.Unix(1700000000, 0)

func sized(size int64) Prober {
	return proberFor("/bin/vigil", fakeInfo{size: size, mod: epoch}, nil)
}

func failing() Prober {
	return proberFor("/bin/vigil", nil, errors.New("boom"))
}

// pastFloor is a watcher stamped at size 100 that started long enough ago to
// be allowed to act.
func pastFloor() Watcher {
	return NewWatcher(sized(100), epoch.Add(-time.Hour))
}

// twice runs the two checks a changed stamp needs, an interval apart so the
// second is not rate-limited away, and returns the second's answer.
func twice(w *Watcher, start time.Time) bool {
	w.Replaced(start)
	return w.Replaced(start.Add(CheckInterval + time.Second))
}

func TestAnUnchangedBinaryIsNotReplaced(t *testing.T) {
	w := pastFloor()
	if twice(&w, epoch) {
		t.Fatal("an unchanged binary reported as replaced")
	}
}

func TestAChangedBinaryIsReplaced(t *testing.T) {
	w := pastFloor()
	w.Prober = sized(200)
	if !twice(&w, epoch) {
		t.Fatal("a changed binary was not reported as replaced")
	}
	if w.OnDisk().Size != 200 {
		t.Fatalf("OnDisk().Size = %d, want 200", w.OnDisk().Size)
	}
}

// `make build` writes ./vigil in place, so a stat can land mid-write and see a
// new mtime with a short size. Exec'ing that kills the process.
func TestAChangedStampSeenOnceIsNotReplaced(t *testing.T) {
	w := pastFloor()
	w.Prober = sized(200)
	if w.Replaced(epoch) {
		t.Fatal("replaced on a single observation; a torn read would exec a truncated binary")
	}
}

// A file still being written grows between checks, so the pair never matches.
func TestAStampThatKeepsChangingNeverConfirms(t *testing.T) {
	w := pastFloor()
	w.Prober = sized(200)
	w.Replaced(epoch)
	w.Prober = sized(300)
	if w.Replaced(epoch.Add(CheckInterval + time.Second)) {
		t.Fatal("two different changed stamps confirmed each other")
	}
	if !w.Replaced(epoch.Add(2*CheckInterval + 2*time.Second)) {
		t.Fatal("a stamp that then held steady for two checks never confirmed")
	}
}

// A changed stamp seen once, then the original back, then the changed one
// again is two observations of a change with the original between them. That
// is not two consecutive checks, so it must not confirm.
func TestARevertBetweenTwoObservationsResetsTheConfirmation(t *testing.T) {
	w := pastFloor()
	w.Prober = sized(200)
	w.Replaced(epoch)
	w.Prober = sized(100)
	w.Replaced(epoch.Add(CheckInterval + time.Second))
	w.Prober = sized(200)
	if w.Replaced(epoch.Add(2*CheckInterval + 2*time.Second)) {
		t.Fatal("confirmed a change across a check that saw the original binary")
	}
}

// A startup probe that failed leaves atStart zero. Zero is "unknown", not a
// prior value: comparing a later successful stamp against it would restart a
// binary that never changed.
func TestAFailedStartupStampIsAdoptedRatherThanTreatedAsAChange(t *testing.T) {
	w := NewWatcher(failing(), epoch.Add(-time.Hour))
	w.Prober = sized(100)
	if twice(&w, epoch) {
		t.Fatal("a failed startup stamp was read as a changed binary")
	}
	if w.AtStart().Size != 100 {
		t.Fatalf("AtStart() = %+v, want the first stamp the process could get", w.AtStart())
	}
}

func TestAFailedStatIsReadAsUnchanged(t *testing.T) {
	w := pastFloor()
	w.Prober = failing()
	if twice(&w, epoch) {
		t.Fatal("a stat failure reported a replacement; it must fail closed")
	}
	if w.OnDisk().Size != 100 {
		t.Fatal("a stat failure overwrote the last good on-disk stamp")
	}
}

// A check inside the window must not stat at all. Observable as the pending
// stamp not being set: had the 1s check run, the next one would confirm.
func TestTheCheckIsRateLimited(t *testing.T) {
	w := pastFloor()
	w.Replaced(epoch)
	w.Prober = sized(200)
	w.Replaced(epoch.Add(time.Second))
	if w.Replaced(epoch.Add(CheckInterval + time.Second)) {
		t.Fatal("the check ran again within the rate limit window")
	}
	if !w.Replaced(epoch.Add(2*CheckInterval + 2*time.Second)) {
		t.Fatal("the check never ran again after the rate limit window")
	}
}

func TestAFreshlyStartedProcessIsNotReplaced(t *testing.T) {
	w := NewWatcher(sized(100), epoch)
	w.Prober = sized(200)
	if twice(&w, epoch) {
		t.Fatal("replaced within the startup floor; a bad stamp would spin")
	}
	if !w.Replaced(epoch.Add(RestartFloor + time.Second)) {
		t.Fatal("still not replaced once the floor had passed")
	}
}
