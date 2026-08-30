// Package app is the service layer shared by the CLI and the TUI.
//
// Every operation that matters lives here, not in a Cobra command function and
// not in a Bubble Tea update handler. The two front ends differ only in how
// they present what these services do.
package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
)

// Sentinel errors the front ends recognise and render specially.
var (
	// ErrNoDevice means no HDD is configured.
	ErrNoDevice = errors.New("no PS2 HDD is configured")
	// ErrNotFound means a named game is not in the library.
	ErrNotFound = errors.New("game not found")
	// ErrCancelled means the user or a signal stopped the operation.
	ErrCancelled = errors.New("operation cancelled")
	// ErrAlreadyInstalled means the title is already on the HDD.
	ErrAlreadyInstalled = errors.New("game is already installed")
	// ErrNotReady means PS1 support is not set up.
	ErrNotReady = errors.New("PS1 support is not ready")
	// ErrDryRun is never returned; it exists so callers can document that a
	// path is inert.
	ErrDryRun = errors.New("dry run")
)

// MissingToolError says a feature is unavailable because an external tool is
// not installed.
//
// This is a setup gap, not a failure: nothing went wrong, a capability simply
// is not there. Front ends are expected to present it differently from an
// error -- calmly, once, with the fix -- because a missing optional tool is a
// stable condition that will be reported on every single refresh until the
// user does something about it, and a red banner that never goes away trains
// people to ignore red banners.
type MissingToolError struct {
	// Tool is the executable that is absent, e.g. "pfsfuse".
	Tool string
	// Feature is what cannot be done without it, phrased as a noun so it
	// reads in a sentence: "artwork status".
	Feature string
}

func (e *MissingToolError) Error() string {
	return fmt.Sprintf("%s needs %s, which is not installed", e.Feature, e.Tool)
}

// Advice returns the one-line fix.
func (e *MissingToolError) Advice() string {
	return fmt.Sprintf("Install %s, then press r to refresh. See docs/dependencies.md.", e.Tool)
}

// Unwrap lets errors.Is find the underlying sentinel.
func (e *MissingToolError) Unwrap() error { return external.ErrToolMissing }

// AsMissingTool returns the MissingToolError in err, if there is one.
func AsMissingTool(err error) (*MissingToolError, bool) {
	var m *MissingToolError
	ok := errors.As(err, &m)
	return m, ok
}

// IsSetupGap reports whether an error is a missing capability rather than a
// malfunction. Front ends use it to choose a tone.
func IsSetupGap(err error) bool {
	return errors.Is(err, external.ErrToolMissing)
}

// missingTool wraps an error as a MissingToolError when it was caused by an
// absent executable, and returns it unchanged otherwise.
func missingTool(err error, tool, feature string) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, external.ErrToolMissing) {
		return &MissingToolError{Tool: tool, Feature: feature}
	}
	return err
}

// partialAwareMissingTool applies missingTool to the error inside a
// *catalog.PartialError without discarding the wrapper.
//
// The wrapper is what tells a caller the library it also received is real but
// incomplete. Rewriting a partial pfsfuse failure into a bare MissingToolError
// would throw that away and make an incomplete read look like a total one.
func partialAwareMissingTool(err error, tool, feature string) error {
	var p *catalog.PartialError
	if errors.As(err, &p) {
		return &catalog.PartialError{Err: missingTool(p.Err, tool, feature)}
	}
	return missingTool(err, tool, feature)
}

// AmbiguousError is returned when a query names more than one title. It is a
// distinct type because the right response is to show the candidates and stop,
// never to pick one.
type AmbiguousError struct {
	Query   string
	Matches []model.Game
}

func (e *AmbiguousError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d games:", e.Query, len(e.Matches))
	for _, g := range e.Matches {
		fmt.Fprintf(&b, "\n  %-4s %-14s %s", g.Platform.Label(), g.GameID, g.Title)
	}
	b.WriteString("\n\nName one of them by its game ID.")
	return b.String()
}

// NotFoundError names what was looked for.
type NotFoundError struct {
	Query string
}

func (e *NotFoundError) Error() string {
	return fmt.Sprintf("no installed or available game matches %q", e.Query)
}

func (e *NotFoundError) Unwrap() error { return ErrNotFound }

// InsufficientSpaceError reports that a game will not fit.
type InsufficientSpaceError struct {
	Title  string
	Needed int64
	Free   int64
}

func (e *InsufficientSpaceError) Error() string {
	return fmt.Sprintf("%s needs %s but only %s is free on the HDD",
		e.Title, model.HumanSize(e.Needed), model.HumanSize(e.Free))
}
