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
