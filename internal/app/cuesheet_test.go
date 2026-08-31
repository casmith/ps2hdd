package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRawRip lays down a file shaped like a MODE2/2352 CD rip, with the ISO
// 9660 primary volume descriptor where such an image keeps it.
func writeRawRip(t *testing.T, dir, name string, sectors int, withDescriptor bool) string {
	t.Helper()
	p := filepath.Join(dir, name)
	data := make([]byte, sectors*rawSectorSize)
	if withDescriptor {
		copy(data[16*rawSectorSize+rawSectorHeader:], []byte("\x01CD001"))
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// hdl_dump's ISO probe looks for the descriptor at 0x8000 -- sector 16 counted
// in 2048-byte sectors. A MODE2/2352 rip keeps it at 0x9318 instead, so the
// probe fails, no other reader handles a bare .bin, and the file is refused
// with "Input or output is unsupported". The cuesheet is what makes it
// readable, and it holds nothing the file does not already say.
func TestEnsureCuesheetWritesOneForARawRip(t *testing.T) {
	dir := t.TempDir()
	bin := writeRawRip(t, dir, "SLUS-20212 (1.00).bin", 20, true)

	if err := ensureCuesheet(bin); err != nil {
		t.Fatal(err)
	}
	cue := filepath.Join(dir, "SLUS-20212 (1.00).cue")
	body, err := os.ReadFile(cue)
	if err != nil {
		t.Fatalf("no cuesheet was written: %v", err)
	}
	// hdl_dump accepts TRACK 01 MODE2/2352 and reads the image from there.
	for _, want := range []string{`FILE "SLUS-20212 (1.00).bin" BINARY`, "TRACK 01 MODE2/2352", "INDEX 01 00:00:00"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("cuesheet is missing %q:\n%s", want, body)
		}
	}
	// The FILE line names the file beside it, not a path: hdl_dump resolves it
	// relative to the cuesheet, and an absolute path would break the moment
	// the scratch directory is different.
	if strings.Contains(string(body), dir) {
		t.Errorf("cuesheet carries an absolute path:\n%s", body)
	}
	// HDLSourcePath now finds it, which is what gets handed to hdl_dump.
	if got := HDLSourcePath(bin); got != cue {
		t.Errorf("HDLSourcePath = %q, want the new cuesheet", got)
	}
}

// Nothing is guessed. A file that is not this shape is left for hdl_dump to
// refuse in its own words rather than wrapped in a cuesheet that lies about it.
func TestEnsureCuesheetLeavesEverythingElseAlone(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		// Not a whole number of raw sectors.
		"ragged.bin": func() string {
			p := filepath.Join(dir, "ragged.bin")
			if err := os.WriteFile(p, make([]byte, 20*rawSectorSize+7), 0o600); err != nil {
				t.Fatal(err)
			}
			return p
		}(),
		// Right size, but no descriptor where a raw rip keeps one.
		"empty.bin": writeRawRip(t, dir, "empty.bin", 20, false),
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ensureCuesheet(p); err != nil {
				t.Fatal(err)
			}
			cue := strings.TrimSuffix(p, ".bin") + ".cue"
			if _, err := os.Stat(cue); !os.IsNotExist(err) {
				t.Error("a cuesheet was invented for a file that is not a raw rip")
			}
		})
	}

	// An ISO needs nothing: hdl_dump reads it directly.
	iso := filepath.Join(dir, "game.iso")
	if err := os.WriteFile(iso, make([]byte, 100), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureCuesheet(iso); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "game.cue")); !os.IsNotExist(err) {
		t.Error("a cuesheet was written beside an ISO")
	}
}

// A rip that shipped with its own cuesheet keeps it: whatever it says about
// pregaps and track modes is better information than a synthesised one.
func TestEnsureCuesheetDoesNotOverwriteAnExistingOne(t *testing.T) {
	dir := t.TempDir()
	bin := writeRawRip(t, dir, "game.bin", 20, true)
	cue := filepath.Join(dir, "game.cue")
	original := "FILE \"game.bin\" BINARY\n  TRACK 01 MODE1/2352\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(cue, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	// HDLSourcePath already resolves to the cuesheet, so that is what is
	// checked -- and a cuesheet is not a .bin, so nothing is written.
	if err := ensureCuesheet(HDLSourcePath(bin)); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(cue)
	if err != nil || string(got) != original {
		t.Errorf("the rip's own cuesheet was replaced:\n%s", got)
	}
}
