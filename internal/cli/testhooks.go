package cli

import "io"

// Test stream overrides.
//
// Cobra's SetOut and SetErr only reach the command's own writers, while the
// commands here write through Env. SetTestStreams lets a test point Env at the
// same buffers. It is exported for the package's own tests and is a no-op in
// normal use.
var (
	testOut io.Writer
	testErr io.Writer
	testIn  io.Reader
)

// SetTestStreams redirects command output. Passing nil restores the defaults.
func SetTestStreams(out, err io.Writer, in io.Reader) {
	testOut, testErr, testIn = out, err, in
}

// applyTestStreams is called during setup so an override takes effect.
func (e *Env) applyTestStreams() {
	if testOut != nil {
		e.Out = testOut
	}
	if testErr != nil {
		e.ErrOut = testErr
	}
	if testIn != nil {
		e.In = testIn
	}
}
