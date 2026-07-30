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

func TestRequestRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	want := &Request{
		Version: Version,
		Type:    RequestDispatch,
		ID:      "job-1",
		Input:   "sc-12345",
		Cwd:     "/Users/x/portal",
	}
	if err := EncodeRequest(&buf, want); err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	got, err := NewRequestDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if *got != *want {
		t.Errorf("got %+v, want %+v", *got, *want)
	}
}

// A request with a version this build does not understand must still decode.
// The daemon has to see it to register a failed job explaining the refusal;
// dropping it at the decoder would look identical to a daemon that never read.
func TestRequestDecoderDoesNotRejectAnUnknownVersion(t *testing.T) {
	r := strings.NewReader(`{"version":99,"type":"dispatch","id":"job-1","input":"sc-1"}` + "\n")
	got, err := NewRequestDecoder(r).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Version != 99 {
		t.Errorf("got version %d, want 99", got.Version)
	}
}

func TestRequestDecoderReturnsEOFWhenExhausted(t *testing.T) {
	d := NewRequestDecoder(strings.NewReader(""))
	if _, err := d.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("got %v, want io.EOF", err)
	}
}

// A malformed line is distinguishable from a dead transport: the line was
// already off the wire, so the caller can call Next again on the same
// decoder rather than treating the connection as gone.
func TestRequestDecoderDistinguishesAMalformedLineFromEOF(t *testing.T) {
	d := NewRequestDecoder(strings.NewReader("not json\n"))
	_, err := d.Next()
	if !errors.Is(err, ErrMalformedRequest) {
		t.Errorf("got %v, want ErrMalformedRequest", err)
	}
	if errors.Is(err, io.EOF) {
		t.Errorf("got %v, want it distinguishable from io.EOF", err)
	}
}

func TestSnapshotCarriesJobs(t *testing.T) {
	var buf bytes.Buffer
	snap := &Snapshot{
		Version:   Version,
		Timestamp: 42,
		Jobs: []Job{{
			ID:     "job-1",
			Input:  "sc-12345",
			State:  JobRunning,
			Status: "classifying story for model routing",
		}},
	}
	if err := Encode(&buf, snap); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := NewDecoder(&buf).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("got %d jobs, want 1", len(got.Jobs))
	}
	if got.Jobs[0].State != JobRunning || got.Jobs[0].Status != "classifying story for model routing" {
		t.Errorf("got %+v", got.Jobs[0])
	}
}

// The no-version-bump decision rests on this: a snapshot written by a daemon
// that predates jobs must decode, with Jobs nil rather than an error.
func TestAJobslessSnapshotStillDecodes(t *testing.T) {
	line := `{"version":1,"timestamp":42,"sessions":[]}` + "\n"
	got, err := NewDecoder(strings.NewReader(line)).Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got.Jobs != nil {
		t.Errorf("got Jobs %v, want nil", got.Jobs)
	}
}

// And the other direction: jobs must not appear in the wire format at all when
// there are none, so an old client sees a byte-identical frame.
func TestNoJobsMeansNoJobsKey(t *testing.T) {
	var buf bytes.Buffer
	if err := Encode(&buf, &Snapshot{Version: Version, Timestamp: 42}); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if strings.Contains(buf.String(), "jobs") {
		t.Errorf("frame mentions jobs: %s", buf.String())
	}
}
