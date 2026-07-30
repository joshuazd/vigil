package fetch

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Commander abstracts subprocess execution for testability.
type Commander interface {
	// Run executes a command in the given directory and returns stdout.
	Run(ctx context.Context, dir string, name string, args ...string) (string, error)
}

// StreamCommander runs a subprocess and delivers its output a line at a time.
// A separate interface rather than a method on Commander: every existing fake
// implements Commander, and only the daemon's job runner needs streaming.
//
// onLine is called from a goroutine that is not the caller's. A caller that
// writes shared state from it must synchronize.
type StreamCommander interface {
	RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error
}

// ExecCommander is the real implementation using os/exec.
type ExecCommander struct {
	Timeout time.Duration // default 10s
}

func (c *ExecCommander) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	// If the caller already set a deadline on ctx, respect it.
	// Otherwise apply the default timeout (10s).
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		timeout := c.Timeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n\r"), err
}

// streamWaitDelay bounds how long Wait may block after cancellation. Both
// output streams are an io.PipeWriter, so os/exec copies through a goroutine
// that ends only when every descendant holding the inherited fd closes it.
// The process group kill below is what normally releases it; this is the
// backstop for a descendant that escaped the group (its own setsid), and
// without a non-zero delay Wait has no bound at all.
const streamWaitDelay = 2 * time.Second

func (c *ExecCommander) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	// The child leads its own process group, and cancellation signals the
	// group rather than the one process. exec.CommandContext kills only the
	// direct sh, and a hook that backgrounds work - which the dispatch chain
	// does - leaves grandchildren holding the output pipe, so a 50ms deadline
	// measured 30s of wall clock before this. dispatch_timeout bounded
	// nothing, jobs are serialized behind one another, and Run waits on the
	// job goroutine, so the daemon could not be shut down either.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killProcessGroup(cmd) }
	cmd.WaitDelay = streamWaitDelay

	pr, pw := io.Pipe()
	// One pipe for both streams: hooks run under `sh -c 'exec 2>&1; ...'`
	// so they are already merged, and anything else that writes to stderr
	// is output the caller wants to see rather than lose.
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return err
	}

	scanned := make(chan struct{})
	go func() {
		defer close(scanned)
		s := bufio.NewScanner(pr)
		s.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for s.Scan() {
			onLine(s.Text())
		}
	}()

	waitErr := cmd.Wait()
	// Closing the write end is what ends the scanner; without it the
	// goroutine blocks forever and this function never returns.
	_ = pw.Close()
	<-scanned
	_ = pr.Close()
	return waitErr
}

// killProcessGroup signals the child's whole process group, which Setpgid
// above made the child the leader of.
//
// The pid guard is load-bearing rather than defensive noise: kill(-1) means
// "every process this user can signal", and vigild runs as that user, so a
// daemon cancelling a hook would kill itself. Only a pid above 1 is ever
// negated. ESRCH means the group is already gone, which is success here.
func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	pid := cmd.Process.Pid
	if pid <= 1 {
		return cmd.Process.Kill()
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return cmd.Process.Kill()
	}
	return nil
}

// MockCommander records calls and returns preset responses.
type MockCommander struct {
	mu           sync.Mutex
	Calls        []MockCall
	Handlers     map[string]MockHandler
	HandlerFuncs map[string]func(ctx context.Context, dir string, args []string) (string, error)
}

type MockCall struct {
	Dir  string
	Name string
	Args []string
}

type MockHandler struct {
	Output string
	Err    error
}

func NewMockCommander() *MockCommander {
	return &MockCommander{Handlers: make(map[string]MockHandler)}
}

func (m *MockCommander) On(name string, output string, err error) {
	m.Handlers[name] = MockHandler{Output: output, Err: err}
}

// OnArgs registers a handler for a specific command+args key.
func (m *MockCommander) OnArgs(key string, output string, err error) {
	m.Handlers[key] = MockHandler{Output: output, Err: err}
}

func (m *MockCommander) Run(ctx context.Context, dir string, name string, args ...string) (string, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, MockCall{Dir: dir, Name: name, Args: args})
	m.mu.Unlock()

	// Try dynamic handler funcs first (same key resolution order)
	if m.HandlerFuncs != nil {
		fullKey := name + " " + strings.Join(args, " ")
		if fn, ok := m.HandlerFuncs[fullKey]; ok {
			return fn(ctx, dir, args)
		}
		if fn, ok := m.HandlerFuncs[name]; ok {
			return fn(ctx, dir, args)
		}
		if len(args) > 0 {
			if fn, ok := m.HandlerFuncs[name+" "+args[0]]; ok {
				return fn(ctx, dir, args)
			}
		}
	}

	// Try exact match with all args
	fullKey := name + " " + strings.Join(args, " ")
	if h, ok := m.Handlers[fullKey]; ok {
		return h.Output, h.Err
	}
	// Try just the command name
	if h, ok := m.Handlers[name]; ok {
		return h.Output, h.Err
	}
	// Try first two args as key (e.g., "git status")
	if len(args) > 0 {
		shortKey := name + " " + args[0]
		if h, ok := m.Handlers[shortKey]; ok {
			return h.Output, h.Err
		}
	}
	return "", nil
}

func (m *MockCommander) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	out, err := m.Run(ctx, dir, name, args...)
	for _, line := range strings.Split(out, "\n") {
		if line != "" {
			onLine(line)
		}
	}
	return err
}
