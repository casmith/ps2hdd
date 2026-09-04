package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/config"
)

func progressEnv(t *testing.T, out, errOut *bytes.Buffer) *Env {
	t.Helper()
	cfg := config.Config{}
	cfg.Sources.PS2 = "/srv/ps2"
	cfg.Sources.PS1 = "/srv/ps1"
	return &Env{Config: cfg, Out: out, ErrOut: errOut}
}

// Progress must never reach stdout. `list --json` pipes its result, and a
// redraw in the middle of the document would corrupt it for every consumer.
func TestScanProgressStaysOffStdout(t *testing.T) {
	SetColor(true)
	t.Cleanup(func() { SetColor(false) })

	var out, errOut bytes.Buffer
	env := progressEnv(t, &out, &errOut)
	report, finish := scanPrinter(env)
	if report == nil {
		t.Fatal("no progress printer on an interactive terminal")
	}
	report(catalog.ScanProgress{Root: "/srv/ps2", Done: 1, Total: 3, Path: "/srv/ps2/Ape Escape.iso"})
	report(catalog.ScanProgress{Root: "/srv/ps2", Done: 3, Total: 3, Path: "/srv/ps2/Zone of the Enders.iso"})
	finish()

	if out.Len() != 0 {
		t.Errorf("progress wrote %q to stdout; it must only ever use stderr", out.String())
	}
	got := errOut.String()
	if !strings.Contains(got, "3/3") {
		t.Errorf("stderr never showed the final count:\n%q", got)
	}
	if !strings.Contains(got, "Zone of the Enders.iso") {
		t.Errorf("stderr never named the file the scan reached:\n%q", got)
	}
	if !strings.Contains(got, "PS2 sources") {
		t.Errorf("stderr does not say which library is being scanned:\n%q", got)
	}
	// finish clears the line so the next thing printed starts on a clean row.
	if !strings.HasSuffix(got, "\r\033[K") {
		t.Errorf("the progress line was not cleared when the scan ended:\n%q", got)
	}
}

// The final report is always painted, even when the whole scan finishes inside
// one redraw interval; otherwise a fast scan appears to stop short of its own
// total, which reads as a failure.
func TestScanProgressAlwaysPaintsTheLastReport(t *testing.T) {
	SetColor(true)
	t.Cleanup(func() { SetColor(false) })

	var out, errOut bytes.Buffer
	report, _ := scanPrinter(progressEnv(t, &out, &errOut))
	for i := 1; i <= 50; i++ {
		report(catalog.ScanProgress{Root: "/srv/ps1", Done: i, Total: 50, Path: "/srv/ps1/game.cue"})
	}
	if !strings.Contains(errOut.String(), "50/50") {
		t.Errorf("the last report was throttled away:\n%q", errOut.String())
	}
	if !strings.Contains(errOut.String(), "PS1 sources") {
		t.Errorf("the PS1 root was not labelled:\n%q", errOut.String())
	}
}

// Nothing is printed when output is not a terminal or the user asked for
// quiet, and a nil report is what the scanner takes to mean "do not report".
func TestScanProgressSilentWhenNotInteractive(t *testing.T) {
	SetColor(false)
	var out, errOut bytes.Buffer
	if report, _ := scanPrinter(progressEnv(t, &out, &errOut)); report != nil {
		t.Error("a non-terminal stderr still got a progress printer")
	}

	SetColor(true)
	t.Cleanup(func() { SetColor(false) })
	env := progressEnv(t, &out, &errOut)
	env.Quiet = true
	if report, _ := scanPrinter(env); report != nil {
		t.Error("--quiet still got a progress printer")
	}
	if out.Len() != 0 || errOut.Len() != 0 {
		t.Errorf("something was printed anyway: out=%q err=%q", out.String(), errOut.String())
	}
}

// A long filename is shortened from the middle, because the disc number at the
// end is the part that says which of four files is being read.
func TestScanProgressKeepsTheDiscNumberVisible(t *testing.T) {
	SetColor(true)
	t.Cleanup(func() { SetColor(false) })
	var out, errOut bytes.Buffer
	report, _ := scanPrinter(progressEnv(t, &out, &errOut))
	report(catalog.ScanProgress{
		Root: "/srv/ps1", Done: 1, Total: 1,
		Path: "/srv/ps1/Final Fantasy VII (USA) (Rev 1) (Squaresoft Collectors Edition) (Disc 3).zip",
	})
	got := errOut.String()
	if !strings.Contains(got, "(Disc 3).zip") {
		t.Errorf("the disc number was truncated away:\n%q", got)
	}
	if !strings.Contains(got, "Final Fantasy") {
		t.Errorf("the title was truncated away:\n%q", got)
	}
}
