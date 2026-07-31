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
	"os"
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

// DefaultAckTimeout bounds the wait for the submitted job to appear in a
// snapshot.
//
// 15s, not the 5s this shipped with. A daemon binds its socket before its
// first poll, so a cold spawn accepts the submission immediately but cannot
// service it until that poll returns - git across every session plus a gh
// call per branch, each under ExecCommander's own 10s ceiling. The daemon now
// publishes as soon as it accepts a submission, so the common path acks in
// milliseconds and this bound is only ever reached by a genuinely stuck
// daemon; a cold one that is merely slow used to spend that time telling the
// user it might be running an older vigil.
const DefaultAckTimeout = 15 * time.Second

type Options struct {
	Input      string
	Cwd        string
	SocketPath string
	// Spawn starts a daemon when none answers. Nil means do not try.
	Spawn      func() error
	AckTimeout time.Duration
	// Detached asks the daemon to skip the teleport, for queue-originated
	// dispatches where the user is mid-edit elsewhere.
	Detached bool
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
		Version:  protocol.Version,
		Type:     protocol.RequestDispatch,
		ID:       id,
		Input:    input,
		Cwd:      opts.Cwd,
		Detached: opts.Detached,
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
		timeout = DefaultAckTimeout
	}
	_ = conn.SetReadDeadline(time.Now().Add(timeout))
	dec := protocol.NewDecoder(conn)
	seen := 0
	for {
		snap, err := dec.Next()
		if err != nil {
			return nil, ackFailure(err, seen, timeout)
		}
		seen++
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

// ackFailure names the diagnosis instead of guessing skew every time. The
// four cases are genuinely different repairs, and only one of them is "make
// install and restart the daemon":
//
//   - snapshots arrived, then the wait expired: the daemon is alive, reading
//     and broadcasting, so it speaks the protocol. Whatever went wrong is on
//     its side of the submission and is in its log.
//   - snapshots arrived, then the stream ended: the daemon exited or crashed
//     mid-job.
//   - nothing arrived and the wait expired: the only case an older vigil
//     explains, since one that never reads request frames also never
//     publishes a job. A daemon still in its first poll looks identical from
//     here, so both are offered.
//   - nothing arrived and the stream ended: the connection failed outright.
func ackFailure(err error, seen int, timeout time.Duration) error {
	expired := errors.Is(err, os.ErrDeadlineExceeded)
	switch {
	case seen > 0 && expired:
		return fmt.Errorf("%w within %s; the daemon is alive and broadcasting but never published the job - check its log", ErrNoAck, timeout)
	case seen > 0:
		return fmt.Errorf("%w: the daemon stopped sending after %d snapshots (%v)", ErrNoAck, seen, err)
	case expired:
		return fmt.Errorf("%w within %s; it sent no snapshot at all, so it is either still starting up or running an older vigil", ErrNoAck, timeout)
	default:
		return fmt.Errorf("%w: the connection failed before any snapshot arrived (%v)", ErrNoAck, err)
	}
}

func newID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating a job id: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
