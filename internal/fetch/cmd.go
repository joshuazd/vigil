package fetch

import (
	"context"
	"os/exec"
	"strings"
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
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.Output()
	return strings.TrimRight(string(out), "\n\r"), err
}

// MockCommander records calls and returns preset responses.
type MockCommander struct {
	Calls    []MockCall
	Handlers map[string]MockHandler
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
	m.Calls = append(m.Calls, MockCall{Dir: dir, Name: name, Args: args})

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
