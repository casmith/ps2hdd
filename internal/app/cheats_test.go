package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// CHEATS.TXT is the user's file: their own codes and per-game tuning live
// there. An install that rewrote it would throw those away, so the directive
// is added to what is already present.
func TestAddCheatPreservesWhatIsThere(t *testing.T) {
	p := filepath.Join(t.TempDir(), ps1.CheatsFile)

	// Absent: created with just the directive.
	if err := addCheat(p, ps1.Widescreen); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != ps1.Widescreen+"\n" {
		t.Fatalf("new file = %q", got)
	}

	// Present and already carrying it: untouched, so a reinstall does not
	// stack duplicates.
	if err := addCheat(p, ps1.Widescreen); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); got != ps1.Widescreen+"\n" {
		t.Errorf("rerun = %q, want no second copy", got)
	}

	// Present with the user's own content: appended to, nothing lost.
	existing := "$SAFEMODE\n$D0012345 0000\n"
	if err := os.WriteFile(p, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addCheat(p, ps1.Widescreen); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), existing+ps1.Widescreen+"\n"; got != want {
		t.Errorf("appended = %q, want %q", got, want)
	}
}

// A file the user left without a trailing newline must not have the directive
// welded onto its last line, where POPStarter would read neither.
func TestAddCheatTerminatesTheLastLineFirst(t *testing.T) {
	p := filepath.Join(t.TempDir(), ps1.CheatsFile)
	if err := os.WriteFile(p, []byte("$SAFEMODE"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addCheat(p, ps1.Widescreen); err != nil {
		t.Fatal(err)
	}
	if got, want := read(t, p), "$SAFEMODE\n"+ps1.Widescreen+"\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Windows line endings and stray spacing are how a hand-edited file actually
// arrives; neither may fool the already-present check into adding a second.
func TestAddCheatRecognisesAnUntidyExistingLine(t *testing.T) {
	for _, body := range []string{
		"$SAFEMODE\r\n" + ps1.Widescreen + "\r\n",
		"$SAFEMODE\n  " + ps1.Widescreen + "  \n",
		"$SAFEMODE\n$widescreen\n",
	} {
		p := filepath.Join(t.TempDir(), ps1.CheatsFile)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := addCheat(p, ps1.Widescreen); err != nil {
			t.Fatal(err)
		}
		if got := read(t, p); got != body {
			t.Errorf("%q gained a duplicate: %q", body, got)
		}
	}
}

// Removal takes back the file ps2hdd wrote and leaves the one the user made
// their own, which is what decides whether the directory goes too.
func TestRemoveIfOnlyOurs(t *testing.T) {
	dir := t.TempDir()
	ours := filepath.Join(dir, "ours.txt")
	theirs := filepath.Join(dir, "theirs.txt")
	if err := os.WriteFile(ours, []byte(ps1.Widescreen+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(theirs, []byte("$SAFEMODE\n"+ps1.Widescreen+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	removeIfOnlyOurs(ours, ps1.Widescreen)
	removeIfOnlyOurs(theirs, ps1.Widescreen)
	if _, err := os.Stat(ours); !os.IsNotExist(err) {
		t.Error("a file holding only our directive survived removal")
	}
	if _, err := os.Stat(theirs); err != nil {
		t.Error("a file the user had added to was deleted")
	}
	// A missing file is not an error.
	removeIfOnlyOurs(filepath.Join(dir, "absent.txt"), ps1.Widescreen)
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
