// Package dispatch submits a dispatch job to vigild. The job runs in the
// daemon, so this is a submission client and nothing more: exit 0 means
// accepted, not succeeded.
package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// ErrNoAck means no snapshot ever carried the submitted job. The likely cause
// is a daemon that predates request frames and therefore never read it.
var ErrNoAck = errors.New("daemon did not accept the job")

const maxInput = 500

// dialTimeout matches the client dial elsewhere: a local unix socket answers
// in microseconds or not at all.
const dialTimeout = 300 * time.Millisecond

// spawnSettle bounds how long Submit waits for a freshly spawned daemon to
// bind its socket before giving up.
const spawnSettle = 3 * time.Second

type Options struct {
	Input      string
	Cwd        string
	SocketPath string
	// Spawn starts a daemon when none answers. Nil means do not try.
	Spawn      func() error
	AckTimeout time.Duration
}

// Validate rejects input before it reaches the daemon, so a malformed
// submission never becomes a job at all.
func Validate(input string) error {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return errors.New("dispatch input must not be empty")
	}
	if len(trimmed) > maxInput {
		return fmt.Errorf("dispatch input too long (%d characters, limit %d)", len(trimmed), maxInput)
	}
	for _, c := range trimmed {
		if c < ' ' && c != '\t' {
			return errors.New("dispatch input contains control characters")
		}
	}
	return nil
}

func Submit(ctx context.Context, opts Options) (*protocol.Job, error) {
	if err := Validate(opts.Input); err != nil {
		return nil, err
	}
	input := strings.TrimSpace(opts.Input)

	id, err := newID()
	if err != nil {
		return nil, err
	}

	conn, err := connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()

	req := &protocol.Request{
		Version: protocol.Version,
		Type:    protocol.RequestDispatch,
		ID:      id,
		Input:   input,
		Cwd:     opts.Cwd,
	}
	if err := protocol.EncodeRequest(conn, req); err != nil {
		return nil, fmt.Errorf("submitting the job: %w", err)
	}

	return awaitAck(conn, id, opts.AckTimeout)
}

// connect dials, and on failure spawns a daemon and retries until it binds.
func connect(ctx context.Context, opts Options) (net.Conn, error) {
	if conn, err := net.DialTimeout("unix", opts.SocketPath, dialTimeout); err == nil {
		return conn, nil
	}
	if opts.Spawn == nil {
		return nil, fmt.Errorf("no daemon listening on %s", opts.SocketPath)
	}
	if err := opts.Spawn(); err != nil {
		return nil, fmt.Errorf("starting a daemon: %w", err)
	}
	deadline := time.Now().Add(spawnSettle)
	for {
		if conn, err := net.DialTimeout("unix", opts.SocketPath, dialTimeout); err == nil {
			return conn, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("started a daemon but nothing is listening on %s", opts.SocketPath)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// awaitAck reads snapshots until the submitted job appears. The snapshot is
// the ack: there is no response frame, which is what makes a refusal visible
// in every panel rather than only here.
func awaitAck(conn net.Conn, id string, timeout time.Duration) (*protocol.Job, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	dec := protocol.NewDecoder(conn)
	for {
		snap, err := dec.Next()
		if err != nil {
			return nil, fmt.Errorf("%w; it may be running an older vigil", ErrNoAck)
		}
		for i := range snap.Jobs {
			job := snap.Jobs[i]
			if job.ID != id {
				continue
			}
			if job.State == protocol.JobRefused {
				return &job, fmt.Errorf("dispatch refused: %s", job.Status)
			}
			return &job, nil
		}
	}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a job id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
