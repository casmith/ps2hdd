package pfs_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/pfs"
)

// The whole point: replacing an existing file must unlink it, not truncate it.
// pfsfuse has no truncate, so a truncating overwrite fails with ENOSYS on
// every file that is already there. A hard link witnesses which happened --
// truncation writes through to every link, unlinking does not.
func TestCreateReplacesRatherThanTruncates(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "POPSTARTER.ELF")
	const original = "the file that was already there"
	if err := os.WriteFile(dest, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	witness := filepath.Join(dir, "witness")
	if err := os.Link(dest, witness); err != nil {
		t.Skipf("hard links unavailable here: %v", err)
	}

	f, err := pfs.Create(dest, 0o644)
	if err != nil {
		t.Fatalf("Create over an existing file: %v", err)
	}
	if _, err := f.WriteString("replacement"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "replacement" {
		t.Errorf("destination = %q", got)
	}
	held, err := os.ReadFile(witness)
	if err != nil {
		t.Fatal(err)
	}
	if string(held) != original {
		t.Errorf("the file was truncated in place; pfsfuse would refuse that.\nwitness holds %q", held)
	}
}

func TestCreateWorksOnANewFile(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "new.png")
	f, err := pfs.Create(dest, 0o644)
	if err != nil {
		t.Fatalf("Create on a new file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

// A destination whose parent does not exist is a caller error, not something
// to paper over by creating directories nobody asked for.
func TestCreateFailsWithoutAParentDirectory(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "absent", "x.png")
	if f, err := pfs.Create(dest, 0o644); err == nil {
		f.Close()
		t.Error("Create invented a parent directory")
	}
}
