// Package logging configures the application log.
//
// ps2hdd logs to a file under the XDG state directory rather than to stderr,
// because the TUI owns the terminal and because the interesting record — which
// device was resolved, which safety checks ran, which external commands were
// executed with which arguments, and how mounts were created and torn down —
// needs to survive the session so a user can hand it to somebody.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/casmith/ps2hdd/internal/config"
)

// Options control log setup.
type Options struct {
	// Verbose raises the level to Debug.
	Verbose bool
	// Debug additionally mirrors the log to stderr. The TUI never sets this;
	// it would corrupt the display.
	Debug bool
	// Stderr is where the mirror is written. Defaults to os.Stderr.
	Stderr io.Writer
}

// closer holds the log file so Close can release it.
var closer io.Closer

// Setup installs the process logger and returns the log file path.
func Setup(opts Options) (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	path := filepath.Join(dir, "ps2hdd.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", fmt.Errorf("open log file: %w", err)
	}
	closer = f

	var w io.Writer = f
	if opts.Debug {
		errw := opts.Stderr
		if errw == nil {
			errw = os.Stderr
		}
		w = io.MultiWriter(f, errw)
	}
	level := slog.LevelInfo
	if opts.Verbose || opts.Debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: level})))
	return path, nil
}

// Close releases the log file.
func Close() error {
	if closer == nil {
		return nil
	}
	err := closer.Close()
	closer = nil
	return err
}

// Discard installs a logger that throws everything away. Tests use it so they
// do not write to the user's real state directory.
func Discard() {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1})))
}

// Path reports where Setup would write, without creating anything.
func Path() (string, error) {
	dir, err := config.StateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ps2hdd.log"), nil
}

// ContextLogger returns the logger to use for an operation, honouring a logger
// stashed in ctx by a caller that wants operation-scoped attributes.
func ContextLogger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

type loggerKey struct{}

// WithLogger returns a context carrying l.
func WithLogger(ctx context.Context, l *slog.Logger) context.Context {
	return context.WithValue(ctx, loggerKey{}, l)
}
