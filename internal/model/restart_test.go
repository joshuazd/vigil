package model

import (
	"errors"
	"io/fs"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
	"github.com/jzinkduda/vigil/internal/protocol"
	"github.com/jzinkduda/vigil/internal/selfbin"
)

type stubInfo struct {
	fs.FileInfo
	size int64
}

func (s stubInfo) Size() int64        { return s.size }
func (s stubInfo) ModTime() time.Time { return time.Unix(0, 0) }

func proberReturning(size int64, err error) selfbin.Prober {
	return selfbin.Prober{
		Executable: func() (string, error) { return "/bin/vigil", nil },
		Stat: func(string) (fs.FileInfo, error) {
			if err != nil {
				return nil, err
			}
			return stubInfo{size: size}, nil
		},
	}
}

// watcherAt is a watcher that started long enough ago to be past the restart
// floor, stamped at size. A size of 0 stamps nothing, as a failed probe does.
func watcherAt(size int64) selfbin.Watcher {
	var err error
	if size == 0 {
		err = errors.New("boom")
	}
	return selfbin.NewWatcher(proberReturning(size, err), time.Now().Add(-time.Hour))
}

// binModel returns a model past the restart floor, stamped at size 100.
func binModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.bin = watcherAt(100)
	return m
}

// checkTwice runs the two checks a changed stamp needs before it can request a
// restart, an interval apart so the second is not rate-limited away.
func checkTwice(m *Model, start time.Time) {
	m.checkBinary(start)
	m.checkBinary(start.Add(selfbin.CheckInterval + time.Second))
}

func TestAnUnchangedBinaryDoesNotRequestARestart(t *testing.T) {
	m := binModel(t)
	checkTwice(&m, time.Now())
	if m.restartRequested {
		t.Fatal("restart requested for an unchanged binary")
	}
}

func TestAChangedBinaryRequestsARestart(t *testing.T) {
	m := binModel(t)
	m.bin.Prober = proberReturning(200, nil)
	checkTwice(&m, time.Now())
	if !m.restartRequested {
		t.Fatal("no restart requested after the binary changed")
	}
}

func TestARestartWaitsForAnOpenPrompt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(m *Model)
	}{
		{"confirm prompt", func(m *Model) { m.confirmAction = ConfirmCleanup }},
		{"dispatch prompt", func(m *Model) { m.dispatchActive = true }},
		{"multi-selection", func(m *Model) { m.selected["alpha"] = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := binModel(t)
			tc.apply(&m)
			m.bin.Prober = proberReturning(200, nil)
			checkTwice(&m, time.Now())
			if m.restartRequested {
				t.Fatalf("restarted with a %s open, losing unsaved intent", tc.name)
			}
		})
	}
}

func TestRestartRequestedIsReadable(t *testing.T) {
	m := binModel(t)
	if m.RestartRequested() {
		t.Fatal("RestartRequested true on a fresh model")
	}
	m.restartRequested = true
	if !m.RestartRequested() {
		t.Fatal("RestartRequested does not report the flag")
	}
}

// restartingModel is one check away from asking to be restarted: the changed
// stamp has already been seen once, a full interval ago, so the next check
// confirms it.
func restartingModel(t *testing.T) Model {
	t.Helper()
	m := binModel(t)
	m.bin.Prober = proberReturning(200, nil)
	if m.bin.Replaced(time.Now().Add(-time.Minute)) {
		t.Fatal("a single observation confirmed a change")
	}
	return m
}

// The flag is inert unless a tick arm turns it into a quit: main only re-execs
// after p.Run() returns, and p.Run() does not return without tea.Quit.
func TestASelfPollTickQuitsWhenTheBinaryChanged(t *testing.T) {
	m := restartingModel(t)

	next, cmd := m.Update(CollectTickMsg{Epoch: m.epoch})

	if !next.(Model).RestartRequested() {
		t.Fatal("a collect tick did not request a restart for a changed binary")
	}
	if !isQuit(cmd) {
		t.Fatal("a collect tick requested a restart without quitting; main never gets to exec")
	}
}

func TestADaemonFedTickQuitsWhenTheBinaryChanged(t *testing.T) {
	m := restartingModel(t)
	m.daemonDecoder = protocol.NewDecoder(strings.NewReader(""))

	next, cmd := m.Update(RenderTickMsg{Epoch: m.epoch})

	if !next.(Model).RestartRequested() {
		t.Fatal("a render tick did not request a restart for a changed binary")
	}
	if !isQuit(cmd) {
		t.Fatal("a render tick requested a restart without quitting; main never gets to exec")
	}
}

func outdatedModel(t *testing.T, onDisk int64) Model {
	t.Helper()
	m := newTestModel()
	m.bin = watcherAt(onDisk)
	m.daemonConn = &net.TCPConn{}
	m.daemonReady = true
	m.lastSnapshot = time.Now()
	m.cfg = &config.Config{}
	return m
}

func TestDaemonHealthReportsAnOutdatedDaemon(t *testing.T) {
	m := outdatedModel(t, 200)
	m.daemonBin = m.bin.OnDisk()
	m.daemonBin.Size = 100
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

func TestDaemonHealthSaysNothingWhenTheDaemonMatchesDisk(t *testing.T) {
	m := outdatedModel(t, 200)
	m.daemonBin = m.bin.OnDisk()
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

// A daemon too old to send the field is too old. Absent reads as outdated.
func TestAnAbsentStampReadsAsOutdated(t *testing.T) {
	m := outdatedModel(t, 200)
	m.daemonBin = selfbin.Stamp{}
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

// The client's own probe failing must not accuse the daemon.
func TestAnUnknownOnDiskStampSaysNothing(t *testing.T) {
	m := outdatedModel(t, 0)
	m.daemonBin = selfbin.Stamp{Size: 100}
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

func TestStalenessOutranksOutdatedness(t *testing.T) {
	m := outdatedModel(t, 200)
	m.daemonBin = selfbin.Stamp{Size: 100}
	m.lastSnapshot = time.Now().Add(-time.Hour)
	if got := m.daemonHealth(); !strings.HasPrefix(got, "daemon stale") {
		t.Fatalf("daemonHealth = %q, want the staleness marker to win", got)
	}
}
