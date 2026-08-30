package external

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
)

// FakeRunner is a recording, scriptable Runner for tests and for `--demo`.
//
// It exists so the install, remove and mount paths can be exercised end to end
// without a PS2 HDD or any of the external tools installed. Nothing in the
// production code path constructs one: the CLI and TUI always receive an
// ExecRunner unless the user explicitly asked for demo mode.
type FakeRunner struct {
	mu sync.Mutex

	// Missing names tools that should report as not installed.
	Missing map[string]bool
	// Responses maps a tool name to canned output, in call order. A tool with
	// no entry returns empty output and success.
	Responses map[string][]Result
	// Errors maps a tool name to errors to return, in call order. A nil entry
	// means that call succeeds.
	Errors map[string][]error
	// Handler, when set, is consulted before Responses and Errors.
	Handler func(c Command) (Result, error)

	calls []Command
}

// NewFakeRunner returns an empty FakeRunner.
func NewFakeRunner() *FakeRunner {
	return &FakeRunner{Missing: map[string]bool{}, Responses: map[string][]Result{}, Errors: map[string][]error{}}
}

// Look implements Runner.
func (f *FakeRunner) Look(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Missing[name] {
		return "", fmt.Errorf("%w: %s", ErrToolMissing, name)
	}
	return "/usr/bin/" + name, nil
}

// Run implements Runner.
func (f *FakeRunner) Run(ctx context.Context, c Command) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	missing := f.Missing[c.Name]
	handler := f.Handler
	var res Result
	var err error
	if rs := f.Responses[c.Name]; len(rs) > 0 {
		res = rs[0]
		f.Responses[c.Name] = rs[1:]
	}
	if es := f.Errors[c.Name]; len(es) > 0 {
		err = es[0]
		f.Errors[c.Name] = es[1:]
	}
	f.mu.Unlock()

	if missing {
		return Result{}, fmt.Errorf("%w: %s", ErrToolMissing, c.Name)
	}
	if handler != nil {
		return handler(c)
	}
	if res.Stdout != "" && c.OnStdout != nil {
		for _, line := range strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n") {
			c.OnStdout(line)
		}
	}
	return res, err
}

// Stream implements Runner by handing fn the canned stdout for the tool.
//
// The fake serves the whole response at once. A real stream arrives in pieces
// and may be cut off mid-read, so a caller that depends on chunk boundaries
// will pass here and fail in the field; read to what you need and no further.
func (f *FakeRunner) Stream(ctx context.Context, c Command, fn func(io.Reader) error) error {
	res, err := f.Run(ctx, c)
	if err != nil {
		return err
	}
	return fn(strings.NewReader(res.Stdout))
}

// Calls returns every command the fake was asked to run.
func (f *FakeRunner) Calls() []Command {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Command, len(f.calls))
	copy(out, f.calls)
	return out
}

// CallsTo returns the commands run for one tool.
func (f *FakeRunner) CallsTo(name string) []Command {
	var out []Command
	for _, c := range f.Calls() {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

// LastArgs returns the arguments of the most recent call to a tool.
func (f *FakeRunner) LastArgs(name string) []string {
	calls := f.CallsTo(name)
	if len(calls) == 0 {
		return nil
	}
	return calls[len(calls)-1].Args
}
