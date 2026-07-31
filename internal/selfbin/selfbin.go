// Package selfbin identifies the image a vigil process is running, so a
// long-lived one can notice it has been replaced on disk.
package selfbin

import (
	"io/fs"
	"os"
)

// Stamp identifies a binary by size and modification time rather than by the
// main.version ldflag: that string comes from `git describe --dirty`, which is
// identical across two consecutive dirty builds - the change that matters most
// during development.
type Stamp struct {
	Size    int64 `json:"size"`
	ModNano int64 `json:"mod_nano"`
}

func (s Stamp) Zero() bool { return s == Stamp{} }

// Prober resolves and stats the running executable. The nil funcs are the real
// ones; a test supplies its own.
type Prober struct {
	Executable func() (string, error)
	Stat       func(string) (fs.FileInfo, error)
}

// Current stamps the path this process was launched from. `make install`
// renames a new file over that path rather than writing in place, so the
// running process keeps its old inode while the path resolves to the new file.
//
// A false second return means "could not tell", and every caller reads it as
// unchanged. Failing closed is the point: a process that cannot stat itself
// must never conclude it is out of date.
func (p Prober) Current() (Stamp, bool) {
	executable := p.Executable
	if executable == nil {
		executable = os.Executable
	}
	stat := p.Stat
	if stat == nil {
		stat = func(name string) (fs.FileInfo, error) { return os.Stat(name) }
	}

	path, err := executable()
	if err != nil || path == "" {
		return Stamp{}, false
	}
	info, err := stat(path)
	if err != nil || info == nil {
		return Stamp{}, false
	}
	return Stamp{Size: info.Size(), ModNano: info.ModTime().UnixNano()}, true
}
