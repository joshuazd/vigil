package fetch

import (
	"context"
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
