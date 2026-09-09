package collect

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jzinkduda/vigil/internal/fetch"
	"github.com/jzinkduda/vigil/internal/session"
)

// countStatusCalls counts `git --no-optional-locks status --porcelain`, the
// one subprocess this whole change exists to move.
func countStatusCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "git" && len(c.Args) == 3 &&
			c.Args[0] == "--no-optional-locks" && c.Args[1] == "status" && c.Args[2] == "--porcelain"
	})
}

// twoSessionsOneRootCommander puts two tmux sessions inside one git root, so a
// store keyed by root costs one status call where one keyed by pane path costs
// two.
func twoSessionsOneRootCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F #{session_created}|#{session_id}|#{session_name}|#{pane_current_path}|#{pane_active}|#{@vigil_claude}|#{@vigil_panel}",
		"1700000000|$1|alpha|/repo/shared\n1700000001|$2|beta|/repo/shared/sub", nil)
	cmd.OnArgs("tmux list-windows -a -F #{session_name}|#{window_bell_flag}", "", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/shared", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
	}
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)
	return cmd
}

// Test 1. The core claim: publication no longer waits on status --porcelain.
func TestSnapshotIssuesNoStatusCalls(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := countStatusCalls(cmd); got != 0 {
		t.Errorf("got %d status calls from a Snapshot, want 0: dirty counts must not block publication", got)
	}
}

// Test 2. And the counts still arrive, one pass later.
func TestCountsLandOnTheSnapshotAfterAPass(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if g := first[0].Git; g.Modified != 0 || g.Added != 0 || g.Deleted != 0 {
		t.Errorf("first Snapshot: got ~%d +%d -%d, want all zero before any pass", g.Modified, g.Added, g.Deleted)
	}

	c.RefreshDirty(ctx)

	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	g := second[0].Git
	if g.Modified != 1 || g.Added != 1 || g.Deleted != 1 {
		t.Errorf("second Snapshot: got ~%d +%d -%d, want ~1 +1 -1", g.Modified, g.Added, g.Deleted)
	}
}

// Test 3. Pins what did NOT move. Relocating unpushedCount or rebaseAge into
// the poller must fail this.
func TestOneSnapshotStillFillsGitMetaWithNoPass(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git rev-parse --verify origin/feature", "abc123", nil)
	cmd.OnArgs("git rev-list origin/feature..HEAD --count", "3", nil)
	cmd.OnArgs("git symbolic-ref refs/remotes/origin/HEAD", "refs/remotes/origin/main", nil)
	cmd.OnArgs("git merge-base origin/main HEAD", "def456", nil)
	cmd.OnArgs("git log -1 --format=%ct def456", "1000000000", nil)

	c := New(noQueueConfig(), cmd)
	sessions, err := c.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	g := sessions[0].Git
	if g.Branch != "feature" {
		t.Errorf("branch: got %q, want feature", g.Branch)
	}
	if g.GitRoot != "/repo/alpha" {
		t.Errorf("gitRoot: got %q, want /repo/alpha", g.GitRoot)
	}
	if g.Unpushed != 3 {
		t.Errorf("unpushed: got %d, want 3", g.Unpushed)
	}
	if g.RebaseAgeSecs == nil {
		t.Error("rebase age: got nil, want a value")
	}
}

// Test 4. Keyed by git root, not pane path.
func TestTwoSessionsInOneRootCostOneStatusCall(t *testing.T) {
	cmd := twoSessionsOneRootCommander()

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	if got := countStatusCalls(cmd); got != 1 {
		t.Errorf("got %d status calls for two sessions in one root, want 1", got)
	}

	sessions, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	for _, s := range sessions {
		if s.Git.Modified != 1 || s.Git.Added != 1 || s.Git.Deleted != 1 {
			t.Errorf("%s: got ~%d +%d -%d, want ~1 +1 -1 - both sessions share the root's answer",
				s.Name, s.Git.Modified, s.Git.Added, s.Git.Deleted)
		}
	}
}

// Test 5. The ordering invariant, named so a failure is legible.
//
// The spec asked for a test that fails alone when fillGit and dirty.track are
// swapped. That is not achievable and the plan says so: Snapshot builds fresh
// sessions every call, so with the lines reversed s.Git.GitRoot is empty at
// track time on every poll, the working set is permanently empty, and every
// other test in this file fails too. This one exists to say which line moved.
func TestFillGitRunsBeforeDirtyTrack(t *testing.T) {
	cmd := singleBranchCommander()

	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	c.dirty.mu.Lock()
	working := append([]string(nil), c.dirty.working...)
	c.dirty.mu.Unlock()

	if len(working) != 1 || working[0] != "/repo/alpha" {
		t.Errorf("got working set %v, want [/repo/alpha]: dirty.track must run after fillGit, which is what puts GitRoot on the session", working)
	}
}

// Test 6. A root that left the working set is pruned at write-back, including
// on a pass where nothing was due.
func TestDirtyPassPrunesEvenWhenNothingWasDue(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	c.dirty.mu.Lock()
	_, present := c.dirty.entries["/repo/alpha"]
	c.dirty.mu.Unlock()
	if !present {
		t.Fatal("fixture is broken: the first pass stored nothing to prune")
	}

	c.dirty.track(nil)
	c.RefreshDirty(ctx)

	c.dirty.mu.Lock()
	n := len(c.dirty.entries)
	c.dirty.mu.Unlock()
	if n != 0 {
		t.Errorf("got %d entries after the root left the working set, want 0", n)
	}
}

// Test 7. An invalidate landing between a pass reading the working set and
// writing it back leaves the entry due, so a forced refresh is not satisfied
// by an answer that may predate it. Mirrors prPoller's gen test.
func TestDirtyPassDoesNotSatisfyAnInvalidateThatLandsDuringIt(t *testing.T) {
	cmd := singleBranchCommander()
	entered := make(chan struct{})
	release := make(chan struct{})
	cmd.HandlerFuncs["git --no-optional-locks status --porcelain"] = func(context.Context, string, []string) (string, error) {
		close(entered)
		<-release
		return " M a.go", nil
	}

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		c.dirty.pass(ctx)
	}()

	waitForEntered(t, entered)
	c.dirty.invalidate()
	close(release)
	waitForDone(t, done)

	c.dirty.mu.Lock()
	e, ok := c.dirty.entries["/repo/alpha"]
	c.dirty.mu.Unlock()
	if !ok {
		t.Fatal("the entry was dropped, not left due")
	}
	if !e.fetchedAt.IsZero() {
		t.Errorf("got fetchedAt %v, want the zero time: an invalidate during the fetch must leave the entry due", e.fetchedAt)
	}
}

// blockingStatusCommander answers every status --porcelain call by blocking on
// release until it is closed, signaling entered the first time it is called
// so a test never has to sleep to know a pass has reached its fetch. Mirrors
// remote_test.go's blockingGhCommander.
func blockingStatusCommander(entered chan struct{}, release chan struct{}, output string) *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.HandlerFuncs = map[string]func(context.Context, string, []string) (string, error){
		"git --no-optional-locks status --porcelain": func(context.Context, string, []string) (string, error) {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-release
			return output, nil
		},
	}
	return cmd
}

// Test 9. The write-back must prune against the working set as it stands
// after the fetch, not as it stood when the fetch started. Mirrors
// prPoller's TestPassDiscardsAResultForABranchThatVanishedMidFetch.
func TestDirtyPassDiscardsAResultForARootThatVanishedMidFetch(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := blockingStatusCommander(entered, release, " M a.go")

	c := New(noQueueConfig(), cmd)
	p := newDirtyPoller(c)
	p.track([]*session.Session{{Git: session.GitStatus{GitRoot: "/repo/alpha"}}})

	done := make(chan struct{})
	go func() {
		p.pass(context.Background())
		close(done)
	}()
	waitForEntered(t, entered)

	// The root vanishes from the working set while its fetch is still in
	// flight - e.g. its session exited.
	p.track(nil)
	close(release)
	waitForDone(t, done)

	p.mu.Lock()
	_, ok := p.entries["/repo/alpha"]
	p.mu.Unlock()
	if ok {
		t.Error("a root that vanished mid-fetch must not survive into the entries map")
	}
}

// Test 10. mu guards the maps, not the subprocess. A concurrent track or fill
// must not wait out a slow status call. Mirrors prPoller's
// TestPassDoesNotHoldMuAcrossTheFetch.
func TestDirtyPassDoesNotHoldMuAcrossTheFetch(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cmd := blockingStatusCommander(entered, release, " M a.go")

	c := New(noQueueConfig(), cmd)
	p := newDirtyPoller(c)
	p.track([]*session.Session{{Git: session.GitStatus{GitRoot: "/repo/alpha"}}})

	done := make(chan struct{})
	go func() {
		p.pass(context.Background())
		close(done)
	}()
	waitForEntered(t, entered)

	trackDone := make(chan struct{})
	go func() {
		p.track([]*session.Session{{Git: session.GitStatus{GitRoot: "/repo/beta"}}})
		close(trackDone)
	}()

	select {
	case <-trackDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("track blocked on mu while a fetch was in flight")
	}

	close(release)
	waitForDone(t, done)
}

// Test 8. -race. Snapshot reads the dirty store while a pass writes it. The
// loop counts are 10x prPoller's equivalent race test: catching the dropped
// -mu mutation (finding 8, task 3 review) needed -count=5 at 50 iterations
// per goroutine, and both goroutines also serialize on MockCommander's own
// mutex, which narrows the window further. 500 iterations catches it
// reliably at -count=1.
func TestSnapshotAndRefreshDirtyAreRaceFree(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = 0

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if _, err := c.Snapshot(ctx); err != nil {
				t.Errorf("Snapshot: %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			c.RefreshDirty(ctx)
		}
	}()
	wg.Wait()
}
