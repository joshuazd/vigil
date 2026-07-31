package selfbin

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeInfo struct {
	fs.FileInfo
	size int64
	mod  time.Time
}

func (f fakeInfo) Size() int64        { return f.size }
func (f fakeInfo) ModTime() time.Time { return f.mod }

func proberFor(path string, info fs.FileInfo, statErr error) Prober {
	return Prober{
		Executable: func() (string, error) { return path, nil },
		Stat:       func(string) (fs.FileInfo, error) { return info, statErr },
	}
}

func TestCurrentReportsSizeAndModTime(t *testing.T) {
	mod := time.Unix(1700000000, 1234)
	got, ok := proberFor("/bin/vigil", fakeInfo{size: 42, mod: mod}, nil).Current()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	if got.Size != 42 || got.ModNano != mod.UnixNano() {
		t.Fatalf("got %+v, want size 42 and mod %d", got, mod.UnixNano())
	}
}

func TestCurrentFailsClosedWhenStatFails(t *testing.T) {
	if _, ok := proberFor("/bin/vigil", nil, errors.New("boom")).Current(); ok {
		t.Fatal("ok = true after a stat failure, want false: the caller reads false as unchanged")
	}
}

func TestCurrentFailsClosedWhenTheExecutableCannotBeResolved(t *testing.T) {
	p := Prober{
		Executable: func() (string, error) { return "", errors.New("boom") },
		Stat:       func(string) (fs.FileInfo, error) { t.Fatal("stat called after Executable failed"); return nil, nil },
	}
	if _, ok := p.Current(); ok {
		t.Fatal("ok = true, want false")
	}
}

func TestZeroDistinguishesAnUnsetStamp(t *testing.T) {
	if !(Stamp{}).Zero() {
		t.Fatal("the zero Stamp is not Zero()")
	}
	if (Stamp{Size: 1}).Zero() {
		t.Fatal("a populated Stamp reports Zero()")
	}
}

// A Prober with no funcs set must work against the real running binary, since
// that is how every non-test caller builds one.
func TestAZeroProberStatsTheRealExecutable(t *testing.T) {
	got, ok := Prober{}.Current()
	if !ok {
		t.Fatal("ok = false for the real test binary")
	}
	if got.Zero() {
		t.Fatal("the real test binary stamped as zero")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path on this platform")
	}
	info, err := os.Stat(filepath.Clean(exe))
	if err != nil {
		t.Skip("cannot stat the test binary")
	}
	if got.Size != info.Size() {
		t.Fatalf("size %d, want %d", got.Size, info.Size())
	}
}
