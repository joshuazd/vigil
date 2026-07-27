package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/jzinkduda/vigil/internal/session"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	snap := &Snapshot{
		Version:   Version,
		Timestamp: 1700000000,
		Sessions: []*session.Session{
			{Name: "alpha", PanePath: "/tmp/alpha", HasBell: true,
				Git: session.GitStatus{Branch: "main", Modified: 2}},
		},
	}

	var buf bytes.Buffer
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Sessions[0].Name != "alpha" {
		t.Errorf("got name %q, want alpha", got.Sessions[0].Name)
	}
	if !got.Sessions[0].HasBell {
		t.Error("bell flag lost in round trip")
	}
	if got.Sessions[0].Git.Modified != 2 {
		t.Errorf("got %d modified, want 2", got.Sessions[0].Git.Modified)
	}
}

func TestEncodeIsNewlineDelimited(t *testing.T) {
	var buf bytes.Buffer
	for i := 0; i < 3; i++ {
		if err := Encode(&buf, &Snapshot{Version: Version}); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}
	if n := strings.Count(buf.String(), "\n"); n != 3 {
		t.Errorf("got %d newlines, want 3", n)
	}
}

func TestDecoderReadsSuccessiveSnapshots(t *testing.T) {
	var buf bytes.Buffer
	for _, ts := range []int64{1, 2, 3} {
		if err := Encode(&buf, &Snapshot{Version: Version, Timestamp: ts}); err != nil {
			t.Fatalf("Encode: %v", err)
		}
	}

	d := NewDecoder(&buf)
	for _, want := range []int64{1, 2, 3} {
		got, err := d.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if got.Timestamp != want {
			t.Errorf("got timestamp %d, want %d", got.Timestamp, want)
		}
	}
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("got %v, want io.EOF", err)
	}
}

func TestDecoderRejectsVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, &Snapshot{Version: Version + 1}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if _, err := NewDecoder(&buf).Next(); !errors.Is(err, ErrVersionMismatch) {
		t.Errorf("got %v, want ErrVersionMismatch", err)
	}
}

func TestDecoderSurvivesLargeSnapshot(t *testing.T) {
	snap := &Snapshot{Version: Version}
	for i := 0; i < 200; i++ {
		snap.Sessions = append(snap.Sessions, &session.Session{
			Name: strings.Repeat("x", 50),
			PR:   &session.PRStatus{Body: strings.Repeat("y", 5000)},
		})
	}
	var buf bytes.Buffer
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(got.Sessions) != 200 {
		t.Errorf("got %d sessions, want 200", len(got.Sessions))
	}
}

func TestSocketPathIsAbsolute(t *testing.T) {
	if p := SocketPath(); !strings.HasPrefix(p, "/") {
		t.Errorf("got %q, want an absolute path", p)
	}
}

func TestSocketPathFallbackToTempDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HOME", "")
	p := SocketPath()
	if !strings.HasPrefix(p, "/") {
		t.Errorf("got %q, want an absolute path even with no XDG_RUNTIME_DIR or HOME", p)
	}
}
