package model

import (
	"errors"
	"io/fs"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/config"
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

// binModel returns a model that has been running long enough to be past the
// startup floor, stamped at size 100.
func binModel(t *testing.T) Model {
	t.Helper()
	m := newTestModel()
	m.startedAt = time.Now().Add(-time.Hour)
	m.binProber = proberReturning(100, nil)
	stamp, ok := m.binProber.Current()
	if !ok {
		t.Fatal("the stub prober failed")
	}
	m.binAtStart = stamp
	m.binOnDisk = stamp
	return m
}

func TestAnUnchangedBinaryDoesNotRequestARestart(t *testing.T) {
	m := binModel(t)
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("restart requested for an unchanged binary")
	}
}

func TestAChangedBinaryRequestsARestart(t *testing.T) {
	m := binModel(t)
	m.binProber = proberReturning(200, nil)
	m.checkBinary(time.Now())
	if !m.restartRequested {
		t.Fatal("no restart requested after the binary changed")
	}
	if m.binOnDisk.Size != 200 {
		t.Fatalf("binOnDisk.Size = %d, want 200", m.binOnDisk.Size)
	}
}

func TestAFailedStatIsReadAsUnchanged(t *testing.T) {
	m := binModel(t)
	m.binProber = proberReturning(0, errors.New("boom"))
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("a stat failure requested a restart; it must fail closed")
	}
	if m.binOnDisk.Size != 100 {
		t.Fatal("a stat failure overwrote the last good on-disk stamp")
	}
}

func TestTheCheckIsRateLimited(t *testing.T) {
	m := binModel(t)
	now := time.Now()
	m.checkBinary(now)
	m.binProber = proberReturning(200, nil)
	m.checkBinary(now.Add(time.Second))
	if m.restartRequested {
		t.Fatal("the check ran again within the rate limit window")
	}
	m.checkBinary(now.Add(binCheckInterval + time.Second))
	if !m.restartRequested {
		t.Fatal("the check never ran again after the rate limit window")
	}
}

func TestAFreshlyStartedProcessDoesNotRestart(t *testing.T) {
	m := binModel(t)
	m.startedAt = time.Now()
	m.binProber = proberReturning(200, nil)
	m.checkBinary(time.Now())
	if m.restartRequested {
		t.Fatal("a process restarted within the startup floor; a bad stamp would spin")
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
			m.binProber = proberReturning(200, nil)
			m.checkBinary(time.Now())
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

func outdatedModel(t *testing.T) Model {
	t.Helper()
	m := binModel(t)
	m.daemonConn = &net.TCPConn{}
	m.daemonReady = true
	m.lastSnapshot = time.Now()
	m.cfg = &config.Config{}
	return m
}

func TestDaemonHealthReportsAnOutdatedDaemon(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 100}
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

func TestDaemonHealthSaysNothingWhenTheDaemonMatchesDisk(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 200}
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

// A daemon too old to send the field is too old. Absent reads as outdated.
func TestAnAbsentStampReadsAsOutdated(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{}
	if got := m.daemonHealth(); got != "daemon outdated" {
		t.Fatalf("daemonHealth = %q, want %q", got, "daemon outdated")
	}
}

// The client's own probe failing must not accuse the daemon.
func TestAnUnknownOnDiskStampSaysNothing(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{}
	m.daemonBin = selfbin.Stamp{Size: 100}
	if got := m.daemonHealth(); got != "" {
		t.Fatalf("daemonHealth = %q, want empty", got)
	}
}

func TestStalenessOutranksOutdatedness(t *testing.T) {
	m := outdatedModel(t)
	m.binOnDisk = selfbin.Stamp{Size: 200}
	m.daemonBin = selfbin.Stamp{Size: 100}
	m.lastSnapshot = time.Now().Add(-time.Hour)
	if got := m.daemonHealth(); !strings.HasPrefix(got, "daemon stale") {
		t.Fatalf("daemonHealth = %q, want the staleness marker to win", got)
	}
}
