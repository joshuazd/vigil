package protocol

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/jzinkduda/vigil/internal/session"
)

const Version = 1

// maxLine bounds a single snapshot. PR bodies and review comments make
// snapshots large, so this is well above bufio's 64KB default.
const maxLine = 8 << 20

// maxRequestLine bounds one client request. Requests are tiny; this is far
// above any legitimate one and exists so a client cannot make the daemon
// allocate without limit.
const maxRequestLine = 64 << 10

const RequestDispatch = "dispatch"

const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
)

var ErrVersionMismatch = errors.New("protocol version mismatch")

// ErrMalformedRequest wraps a Request decode failure that is recoverable: the
// line was already scanned off the wire, so the connection itself is still
// healthy and the caller can keep reading. Only a plain (unwrapped) error from
// Next means the transport is gone.
var ErrMalformedRequest = errors.New("malformed request frame")

func SocketPath() string {
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "vigil", "vigild.sock")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".local", "state", "vigil", "vigild.sock")
	}
	return filepath.Join(os.TempDir(), "vigil", "vigild.sock")
}

type Snapshot struct {
	Version   int                `json:"version"`
	Timestamp int64              `json:"timestamp"`
	Sessions  []*session.Session `json:"sessions"`
	// Jobs is additive on purpose: it is what lets this stay protocol
	// version 1. A client that predates it ignores the key, and a client
	// that expects it sees nil against a daemon that does not send it.
	Jobs []Job `json:"jobs,omitempty"`
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

// Request is a client-to-daemon frame. Only clients write these and only the
// daemon reads them, which is why no envelope is needed to tell them apart
// from a Snapshot.
type Request struct {
	Version int    `json:"version"`
	Type    string `json:"type"`
	ID      string `json:"id"`
	Input   string `json:"input"`
	Cwd     string `json:"cwd"`
}

// Job is one dispatch, as the daemon sees it. Status is the last line the job
// printed, or the reason it failed.
type Job struct {
	ID      string `json:"id"`
	Input   string `json:"input"`
	State   string `json:"state"`
	Status  string `json:"status"`
	Started int64  `json:"started"`
	Ended   int64  `json:"ended"`
}

func EncodeRequest(w io.Writer, req *Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

type RequestDecoder struct {
	scanner *bufio.Scanner
}

func NewRequestDecoder(r io.Reader) *RequestDecoder {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 4*1024), maxRequestLine)
	return &RequestDecoder{scanner: s}
}

// Next deliberately does not check Version. An unrecognized version has to
// reach the daemon so it can answer with a failed job naming the reason;
// refusing here would be indistinguishable from a daemon that never read.
//
// A json.Unmarshal failure is wrapped in ErrMalformedRequest: the line was
// already scanned off the wire, so the connection is still good and a caller
// can call Next again. A scan failure (or EOF) is returned bare, because that
// means the transport itself is gone.
func (d *RequestDecoder) Next() (*Request, error) {
	if !d.scanner.Scan() {
		if err := d.scanner.Err(); err != nil {
			return nil, err
		}
		return nil, io.EOF
	}
	var req Request
	if err := json.Unmarshal(d.scanner.Bytes(), &req); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrMalformedRequest, err)
	}
	return &req, nil
}
