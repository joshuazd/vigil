package fetch

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
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

func (c *ExecCommander) RunStream(ctx context.Context, dir string, env []string, name string, args []string, onLine func(string)) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

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
