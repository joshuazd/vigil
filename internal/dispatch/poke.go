package dispatch

import (
	"net"
	"time"

	"github.com/jzinkduda/vigil/internal/protocol"
)

// pokeDialTimeout bounds the dial. This runs from a tmux hook on every
// session switch, so it must never hang: a lost poke costs one tick of a
// stale highlight, which is exactly the state it was trying to improve.
const pokeDialTimeout = 500 * time.Millisecond

// Poke asks a running daemon to rebroadcast the snapshot it already holds, so
// every client re-resolves which session is current without waiting for the
// next tick.
//
// It deliberately does less than Submit. It does not spawn a daemon: a hook
// that fires on every session switch must not start processes. It does not
// wait for an ack, because unlike a dispatch there is no job id to watch for
// and nothing is lost by a dropped frame. The error is returned for tests;
// main swallows it and exits 0 so the hook stays silent.
func Poke(socketPath string) error {
	conn, err := net.DialTimeout("unix", socketPath, pokeDialTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return protocol.EncodeRequest(conn, &protocol.Request{
		Version: protocol.Version,
		Type:    protocol.RequestPoke,
	})
}
