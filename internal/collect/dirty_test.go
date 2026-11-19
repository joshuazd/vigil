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
	cmd.OnArgs("tmux list-panes -a -F "+fetch.ListPanesFormat,
		"1700000000|$1|alpha|/repo/shared\n1700000001|$2|beta|/repo/shared/sub", nil)
	cmd.OnArgs("tmux list-windows -a -F "+fetch.BellFormat, "", nil)
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

func TestDirtyPassReportsItsSlowestRoot(t *testing.T) {
	const delay = 60 * time.Millisecond
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F "+fetch.ListPanesFormat,
		"1700000000|$1|fast|/tmp/fast\n1700000001|$2|slow|/tmp/slow", nil)
	cmd.OnArgs("tmux list-windows -a -F "+fetch.BellFormat, "", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(_ context.Context, dir string, _ []string) (string, error) {
			return dir, nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "feature", nil
		},
		"git --no-optional-locks status --porcelain": func(_ context.Context, dir string, _ []string) (string, error) {
			if dir == "/tmp/slow" {
				time.Sleep(delay)
			}
			return " M a.go", nil
		},
	}

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	stats := c.DirtyStats()
	if stats.SlowestRoot != "/tmp/slow" {
		t.Errorf("slowest root: got %q, want /tmp/slow", stats.SlowestRoot)
	}
	if stats.Slowest < delay {
		t.Errorf("slowest: got %s, want at least %s", stats.Slowest, delay)
	}
	if stats.Total < delay {
		t.Errorf("total: got %s, want at least %s", stats.Total, delay)
	}
}

// Zero means no work, matching GitStats. A pass that timed an empty fan-out
// and reported a few hundred nanoseconds would make that false, which is the
// exact bug GitStats shipped with and a test caught.
func TestDirtyStatsAreZeroWhenNothingWasDue(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)
	if c.DirtyStats().Total == 0 {
		t.Fatal("the first pass should have measured a fan-out")
	}

	c.RefreshDirty(ctx)
	if got := c.DirtyStats(); got != (DirtyStats{}) {
		t.Errorf("got %+v, want the zero value", got)
	}
}

// branchlessCommander answers tmux and git for one session whose branch is
// empty, the shape of a detached HEAD (an in-progress rebase).
func branchlessCommander() *fetch.MockCommander {
	cmd := fetch.NewMockCommander()
	cmd.OnArgs("tmux list-panes -a -F "+fetch.ListPanesFormat,
		"1700000000|$1|alpha|/repo/alpha", nil)
	cmd.OnArgs("tmux list-windows -a -F "+fetch.BellFormat, "alpha|0", nil)
	cmd.HandlerFuncs = map[string]func(ctx context.Context, dir string, args []string) (string, error){
		"git rev-parse --show-toplevel": func(context.Context, string, []string) (string, error) {
			return "/repo/alpha", nil
		},
		"git branch --show-current": func(context.Context, string, []string) (string, error) {
			return "", nil
		},
	}
	return cmd
}

// TestBranchlessSessionStillGetsDirtyCounts pins the line that decides the
// design doc's Status correction 2: FetchGitMeta's empty-branch early return,
// `return session.GitStatus{GitRoot: gitRoot}`, is what puts a detached-HEAD
// root into dirty.track's working set. Mutating that to session.GitStatus{}
// leaves GitRoot empty too, the working set permanently empty for this
// session, and the counts below never land.
func TestBranchlessSessionStillGetsDirtyCounts(t *testing.T) {
	cmd := branchlessCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go\n?? b.go\n D c.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	first, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	if first[0].Git.GitRoot != "/repo/alpha" {
		t.Fatalf("fixture is broken: got GitRoot %q, want /repo/alpha", first[0].Git.GitRoot)
	}
	if first[0].Git.Branch != "" {
		t.Fatalf("fixture is broken: got branch %q, want empty (detached HEAD)", first[0].Git.Branch)
	}

	c.RefreshDirty(ctx)

	second, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatalf("second Snapshot: %v", err)
	}
	g := second[0].Git
	if g.Modified != 1 || g.Added != 1 || g.Deleted != 1 {
		t.Errorf("second Snapshot: got ~%d +%d -%d, want ~1 +1 -1: a branchless root must still be tracked and filled", g.Modified, g.Added, g.Deleted)
	}
}

// TestInvalidateForcesARefetchOfDirtyCounts is the local half of
// TestInvalidateForcesARefetchOfGitAndPR. It goes through Collector.Invalidate
// rather than dirtyPoller's own invalidate, because the wiring - Invalidate's
// fan-out to c.local - is the thing under test: a local scheduler left out of
// Invalidate would still pass a test that called c.dirty.invalidate() directly.
//
// GitInterval is set high enough that only Invalidate can make the root due
// again inside this test's timeframe, or the assertion would pass even with
// the fan-out deleted.
func TestInvalidateForcesARefetchOfDirtyCounts(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)
	first := countStatusCalls(cmd)
	if first != 1 {
		t.Fatalf("got %d status calls after the first pass, want 1", first)
	}

	c.Invalidate()
	c.RefreshDirty(ctx)

	if got := countStatusCalls(cmd); got <= first {
		t.Errorf("got %d status calls after Invalidate, want more than %d (the entry should have been made due)", got, first)
	}
}

// countWarmCalls counts the *unflagged* `git status --porcelain`, the one
// that lets git write the index back. Distinct from countStatusCalls, which
// counts the --no-optional-locks form the steady state uses.
func countWarmCalls(cmd *fetch.MockCommander) int {
	return cmd.CallCountFunc(func(c fetch.MockCall) bool {
		return c.Name == "git" && len(c.Args) == 2 && c.Args[0] == "status" && c.Args[1] == "--porcelain"
	})
}

// A root vigil has never seen may have a cold untracked cache, and the
// --no-optional-locks form can never warm it - that is the whole defect. One
// unflagged run per root fixes it permanently.
func TestAFirstPassWarmsARootBeforeFetchingIt(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	c.RefreshDirty(ctx)

	if got := countWarmCalls(cmd); got != 1 {
		t.Errorf("got %d warm calls on the first pass, want 1", got)
	}
	if got := countStatusCalls(cmd); got != 1 {
		t.Errorf("got %d flagged status calls, want 1: warming must not replace the fetch", got)
	}
}

// Once per root, not once per pass. Warming takes the index lock, which is
// the contention --no-optional-locks exists to avoid, so it must not become
// the steady state.
func TestWarmingHappensOncePerRootNotOncePerPass(t *testing.T) {
	cmd := singleBranchCommander()
	cmd.OnArgs("git --no-optional-locks status --porcelain", " M a.go", nil)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = time.Hour
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// Invalidate rather than a zero interval: at GitInterval 0 the staleAfter
	// floor makes freshness the fetch's own duration, which is microseconds
	// here, so whether a pass refetches becomes a race. Zeroing fetchedAt is
	// deterministic, and it leaves warmed untouched - which is the point, so
	// the warm gate is the only thing that can hold.
	for i := 0; i < 3; i++ {
		c.Invalidate()
		c.RefreshDirty(ctx)
	}

	if got := countStatusCalls(cmd); got != 3 {
		t.Fatalf("got %d flagged status calls, want 3: the fixture must refetch every pass or this proves nothing", got)
	}
	if got := countWarmCalls(cmd); got != 1 {
		t.Errorf("got %d warm calls across three passes, want 1", got)
	}
}

// slowStatusCommander answers status --porcelain for one root after delay.
// Real sleeps rather than a fake clock, as in stats_test.go: what is under
// test is how a fetch's own wall time feeds back into when it next runs.
func slowStatusCommander(delay time.Duration) *fetch.MockCommander {
	cmd := singleBranchCommander()
	cmd.HandlerFuncs["git --no-optional-locks status --porcelain"] = func(context.Context, string, []string) (string, error) {
		time.Sleep(delay)
		return " M a.go", nil
	}
	return cmd
}

// A fetch slower than git_interval used to be stale the instant it was
// written: fetchedAt recorded when the pass started, so by write-back time it
// was already older than the interval and the very next pass refetched. With
// Snapshot nudging every tick that is a back-to-back loop with no gap, and it
// bites hardest exactly when the machine is too loaded to afford it - measured
// on 2026-09-10 at load average 293, where a status call at ~/pr-36411 spent
// its whole 10s command timeout and was reissued every 10s indefinitely.
func TestASlowRootIsNotRefetchedOnTheNextPass(t *testing.T) {
	const delay = 200 * time.Millisecond
	cmd := slowStatusCommander(delay)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = 50 * time.Millisecond
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 1 {
		t.Fatalf("got %d status calls after the first pass, want 1", got)
	}

	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 1 {
		t.Errorf("got %d status calls, want 1: a root whose fetch took %s must not be due again immediately at a %s interval",
			got, delay, c.GitInterval)
	}
}

// Recording fetchedAt after the fetch is not on its own enough: it only buys
// a slow root one git_interval of gap, so a 10s fetch at a 3s interval still
// spends 10s of every 13s in git. staleAfter's floor is what bounds that to
// half, and the window between the interval and the fetch's own duration is
// the only place the floor is observable - the test above passes without it.
func TestASlowRootsBackoffScalesWithItsOwnFetch(t *testing.T) {
	const delay = 300 * time.Millisecond
	cmd := slowStatusCommander(delay)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = 30 * time.Millisecond
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 1 {
		t.Fatalf("got %d status calls after the first pass, want 1", got)
	}

	// Past git_interval, well short of what the fetch itself cost.
	time.Sleep(100 * time.Millisecond)
	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 1 {
		t.Errorf("got %d status calls, want 1: a root that took %s must stay fresh for %s, not for the %s interval",
			got, delay, delay, c.GitInterval)
	}
}

// The other half, and the reason the tests above cannot be satisfied by simply
// never refetching: the backoff is a delay, not a latch.
func TestASlowRootIsRefetchedOnceItsBackoffElapses(t *testing.T) {
	const delay = 100 * time.Millisecond
	cmd := slowStatusCommander(delay)

	ctx := context.Background()
	c := New(noQueueConfig(), cmd)
	c.GitInterval = 20 * time.Millisecond
	if _, err := c.Snapshot(ctx); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 1 {
		t.Fatalf("got %d status calls after the first pass, want 1", got)
	}

	time.Sleep(delay + 50*time.Millisecond)
	c.RefreshDirty(ctx)
	if got := countStatusCalls(cmd); got != 2 {
		t.Errorf("got %d status calls, want 2: the backoff must expire, not latch", got)
	}
}
