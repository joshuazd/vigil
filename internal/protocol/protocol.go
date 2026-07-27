package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/jzinkduda/vigil/internal/session"
)

const Version = 1

// maxLine bounds a single snapshot. PR bodies and review comments make
// snapshots large, so this is well above bufio's 64KB default.
const maxLine = 8 << 20

var ErrVersionMismatch = errors.New("protocol version mismatch")

func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "vigil", "vigild.sock")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "vigil", "vigild.sock")
}

type Snapshot struct {
	Version   int                `json:"version"`
	Timestamp int64              `json:"timestamp"`
	Sessions  []*session.Session `json:"sessions"`
}

func Encode(w io.Writer, snap *Snapshot) error {
	data, err := json.Marshal(snap)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

type Decoder struct {
	scanner *bufio.Scanner
}

func NewDecoder(r io.Reader) *Decoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxLine)
	return &Decoder{scanner: s}
}

func (d *Decoder) Next() (*Snapshot, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var snap Snapshot
	if err := json.Unmarshal(d.scanner.Bytes(), &snap); err != nil {
		return nil, err
	}
	if snap.Version != Version {
		return nil, ErrVersionMismatch
	}
	return &snap, nil
}
