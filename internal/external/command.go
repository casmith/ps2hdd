// Package external wraps the third-party executables ps2hdd drives.
//
// Every exec.Command in the project lives in this package. That keeps three
// things in one place: the decision of whether a command needs sudo, the
// logging of exactly what was run, and the translation of a bare "exit status
// 1" into an error that names the tool, the arguments and the tool's own
// diagnostic output.
package external

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"

	"github.com/casmith/ps2hdd/internal/logging"
)

// ErrToolMissing means the executable was not found on PATH or at the
// configured location.
var ErrToolMissing = errors.New("external tool not found")

// ToolError carries the failure of an external command.
type ToolError struct {
	Tool   string
	Args   []string
	Err    error
	Stderr string
	Stdout string
}

func (e *ToolError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s failed: %v", e.Tool, e.Err)
	// The tool's own message is nearly always more useful than the exit code,
	// so it goes in the error string rather than only the log.
	if msg := firstUsefulLine(e.Stderr, e.Stdout); msg != "" {
		fmt.Fprintf(&b, ": %s", msg)
	}
	return b.String()
}

func (e *ToolError) Unwrap() error { return e.Err }

// Command describes one invocation.
type Command struct {
	Name string
	Args []string
	// Privileged marks a command that needs raw block device access. The
	// Runner decides whether that means prefixing sudo.
	Privileged bool
	// Stdin, when set, is fed to the process.
	Stdin io.Reader
	// OnStdout, when set, receives stdout a line at a time as it arrives, so
	// progress can be reported without buffering the whole stream.
	OnStdout func(line string)
	// OnStderr is the same for stderr.
	OnStderr func(line string)
}

// Result is the outcome of a completed command.
type Result struct {
	Stdout string
	Stderr string
}

// Runner executes commands. The interface exists so the install and mount
// paths can be tested against a recorded fake without a PS2 HDD.
type Runner interface {
	Run(ctx context.Context, c Command) (Result, error)
	// Look resolves an executable, returning ErrToolMissing when it is absent.
	Look(name string) (string, error)
	// Stream runs a command and hands its stdout to fn while the process is
	// still running.
	//
	// It exists for output too large to buffer. Identifying a game inside a
	// 4 GB archive means reading the first few megabytes of a decompression
	// stream and then stopping, which Run cannot do: it collects everything
	// into a string and waits for the process to exit.
	//
	// Only fn's error is returned. Once fn stops reading early the process is
	// killed, so its exit status describes the kill rather than the work, and
	// treating that as failure would fail every successful partial read. The
	// caller judges the content it got instead. Whatever the tool wrote to
	// stderr is attached to fn's error, so a corrupt archive still explains
	// itself.
	Stream(ctx context.Context, c Command, fn func(io.Reader) error) error
}

// ExecRunner runs commands for real.
type ExecRunner struct {
	// Sudo prefixes privileged commands with sudo. Raw block devices are
	// normally root-owned, and ps2hdd deliberately does not chmod them or ask
	// the user to run the whole TUI as root.
	Sudo bool
	// Overrides maps a tool name to an explicit path from the config file.
	Overrides map[string]string
	// SudoPath overrides the sudo executable; empty means "sudo" on PATH.
	SudoPath string
}

// Look implements Runner.
func (r *ExecRunner) Look(name string) (string, error) {
	if p, ok := r.Overrides[name]; ok && p != "" {
		if _, err := exec.LookPath(p); err != nil {
			return "", fmt.Errorf("%w: %s (configured as %s)", ErrToolMissing, name, p)
		}
		return p, nil
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrToolMissing, name)
	}
	return p, nil
}

// Run implements Runner.
func (r *ExecRunner) Run(ctx context.Context, c Command) (Result, error) {
	path, err := r.Look(c.Name)
	if err != nil {
		return Result{}, err
	}

	argv, err := r.argv(path, c)
	if err != nil {
		return Result{}, err
	}

	log := logging.ContextLogger(ctx)
	log.Info("running external command", "tool", c.Name, "argv", argv)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = c.Stdin

	var outBuf, errBuf bytes.Buffer
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	if err := cmd.Start(); err != nil {
		return Result{}, &ToolError{Tool: c.Name, Args: c.Args, Err: err}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(stdout, &outBuf, c.OnStdout) }()
	go func() { defer wg.Done(); pump(stderr, &errBuf, c.OnStderr) }()
	wg.Wait()

	waitErr := cmd.Wait()
	res := Result{Stdout: outBuf.String(), Stderr: errBuf.String()}
	if waitErr != nil {
		log.Error("external command failed", "tool", c.Name, "err", waitErr,
			"stderr", truncate(res.Stderr, 2000))
		return res, &ToolError{Tool: c.Name, Args: c.Args, Err: waitErr, Stderr: res.Stderr, Stdout: res.Stdout}
	}
	log.Debug("external command finished", "tool", c.Name)
	return res, nil
}

// argv builds the argument vector, prefixing sudo for a privileged command.
func (r *ExecRunner) argv(path string, c Command) ([]string, error) {
	argv := append([]string{path}, c.Args...)
	if !c.Privileged || !r.Sudo {
		return argv, nil
	}
	sudo := r.SudoPath
	if sudo == "" {
		sudo = "sudo"
	}
	sudoPath, err := exec.LookPath(sudo)
	if err != nil {
		return nil, fmt.Errorf("%w: sudo (needed for privileged access to %s)", ErrToolMissing, c.Name)
	}
	// -n so a password prompt fails fast instead of blocking a TUI that has
	// taken over the terminal.
	return append([]string{sudoPath, "-n", "--"}, argv...), nil
}

// Stream implements Runner.
func (r *ExecRunner) Stream(ctx context.Context, c Command, fn func(io.Reader) error) error {
	path, err := r.Look(c.Name)
	if err != nil {
		return err
	}
	argv, err := r.argv(path, c)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	log := logging.ContextLogger(ctx)
	log.Info("streaming external command", "tool", c.Name, "argv", argv)

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin = c.Stdin
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return &ToolError{Tool: c.Name, Args: c.Args, Err: err}
	}

	fnErr := fn(stdout)

	// Closing the read end and cancelling stops a process that is still
	// producing output. Wait must still be called to reap it; its error is
	// deliberately discarded, for the reason given on the interface.
	_ = stdout.Close()
	cancel()
	_ = cmd.Wait()

	if fnErr != nil {
		if stderr := strings.TrimSpace(errBuf.String()); stderr != "" {
			return &ToolError{Tool: c.Name, Args: c.Args, Err: fnErr, Stderr: stderr}
		}
		return fnErr
	}
	return nil
}

// pump copies r into buf while feeding complete lines to onLine.
//
// Progress-reporting tools such as hdl_dump redraw a single line with carriage
// returns rather than newlines, so \r is treated as a line break too.
func pump(r io.Reader, buf *bytes.Buffer, onLine func(string)) {
	if onLine == nil {
		_, _ = io.Copy(buf, r)
		return
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		line := sc.Text()
		buf.WriteString(line)
		buf.WriteByte('\n')
		onLine(line)
	}
}

// scanLinesOrCR splits on \n, \r\n or a bare \r.
func scanLinesOrCR(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
		adv := i + 1
		// Consume the \n of a \r\n pair as part of the same break.
		if data[i] == '\r' && i+1 < len(data) && data[i+1] == '\n' {
			adv++
		}
		return adv, data[:i], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func firstUsefulLine(streams ...string) string {
	for _, s := range streams {
		for _, line := range strings.Split(s, "\n") {
			line = strings.TrimSpace(line)
			if line != "" && !strings.HasPrefix(line, "hdl_dump-") {
				return line
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Available reports whether a tool can be resolved, for `ps2hdd doctor`.
func Available(r Runner, name string) (string, bool) {
	p, err := r.Look(name)
	if err != nil {
		return "", false
	}
	return p, true
}
