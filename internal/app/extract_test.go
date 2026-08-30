package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/model"
)

func TestCueMemberFor(t *testing.T) {
	cases := map[string]struct {
		entries []external.ArchiveEntry
		member  string
		want    string
	}{
		"named for the image": {
			entries: []external.ArchiveEntry{
				{Name: "Gradius V (USA).bin"}, {Name: "Gradius V (USA).cue"},
			},
			member: "Gradius V (USA).bin",
			want:   "Gradius V (USA).cue",
		},
		"a lone sheet describes the lone image": {
			entries: []external.ArchiveEntry{
				{Name: "disc.bin"}, {Name: "Gradius V.cue"},
			},
			member: "disc.bin",
			want:   "Gradius V.cue",
		},
		"no sheet": {
			entries: []external.ArchiveEntry{{Name: "Game (USA).iso"}},
			member:  "Game (USA).iso",
			want:    "",
		},
		// Two sheets and no name match is a packaging decision, not something
		// to pick between: handing hdl_dump the wrong one would describe the
		// wrong sector layout.
		"two sheets, neither named for the image": {
			entries: []external.ArchiveEntry{
				{Name: "disc.bin"}, {Name: "a.cue"}, {Name: "b.cue"},
			},
			member: "disc.bin",
			want:   "",
		},
		"case differs": {
			entries: []external.ArchiveEntry{
				{Name: "Game.BIN"}, {Name: "Game.CUE"},
			},
			member: "Game.BIN",
			want:   "Game.CUE",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := cueMemberFor(tc.entries, tc.member); got != tc.want {
				t.Errorf("cueMemberFor = %q, want %q", got, tc.want)
			}
		})
	}
}

// hdl_dump cannot read a raw MODE2/2352 .bin: its input layer answers "Input
// or output is unsupported". It reads the cuesheet naming that .bin without
// complaint, so the sheet is what must be handed over.
func TestHDLSourcePathPrefersTheCuesheet(t *testing.T) {
	dir := t.TempDir()

	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A .bin with its sheet beside it resolves to the sheet.
	bin := write("Gradius V (USA).bin")
	cue := write("Gradius V (USA).cue")
	if got := HDLSourcePath(bin); got != cue {
		t.Errorf("HDLSourcePath(%q) = %q, want %q", bin, got, cue)
	}

	// A .bin with no sheet is passed through: there is nothing better to hand
	// over, and hdl_dump's own error is more useful than a guess.
	lonely := write("Lonely.bin")
	if got := HDLSourcePath(lonely); got != lonely {
		t.Errorf("HDLSourcePath(%q) = %q, want it unchanged", lonely, got)
	}

	// An ISO is never redirected, even if something cue-shaped sits beside it.
	iso := write("Devil May Cry (USA).iso")
	write("Devil May Cry (USA).cue")
	if got := HDLSourcePath(iso); got != iso {
		t.Errorf("HDLSourcePath(%q) = %q, want it unchanged", iso, got)
	}
}

// A raw CD rip must come out of the archive with its cuesheet, and the path
// handed on must be the sheet. This is the failure that reached a user:
// hdl_dump was given the .bin and answered "Input or output is unsupported".
func TestExtractSourceBringsOutTheCuesheet(t *testing.T) {
	sevenZip := ""
	for _, name := range []string{external.SevenZipTool, external.SevenZipAltTool} {
		if p, err := exec.LookPath(name); err == nil {
			sevenZip = p
			break
		}
	}
	if sevenZip == "" {
		t.Skip("no 7z installed")
	}

	src := t.TempDir()
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "RAW_CD",
		CDXA:     true,
		Files: map[string][]byte{
			"SYSTEM.CNF":  isosynth.PS2SystemCNF("SLUS_207.12"),
			"SLUS_207.12": []byte("ELF"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "Raw (USA).bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cue := "FILE \"Raw (USA).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(src, "Raw (USA).cue"), []byte(cue), 0o600); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "Raw (USA).7z")
	cmd := exec.Command(sevenZip, "a", "-mx=0", "-y", archive, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build archive: %v\n%s", err, out)
	}

	cfg := config.Default()
	cfg.Install.ScratchDir = t.TempDir()
	svc := New(cfg, &external.ExecRunner{})

	g := model.Game{
		Title:         "Raw (USA)",
		GameID:        "SLUS_207.12",
		SizeBytes:     int64(len(data)),
		SourcePath:    archive,
		ArchiveMember: "Raw (USA).bin",
	}
	got, cleanup, err := svc.extractSource(context.Background(), g, InstallOptions{})
	if err != nil {
		t.Fatalf("extractSource: %v", err)
	}
	defer cleanup()

	if filepath.Ext(got) != ".cue" {
		t.Errorf("extractSource returned %q, want the cuesheet", got)
	}
	// The sheet is useless without the image beside it: its FILE line names a
	// bare filename and hdl_dump resolves that in the same directory.
	bin := filepath.Join(filepath.Dir(got), "Raw (USA).bin")
	if fi, err := os.Stat(bin); err != nil || fi.Size() != int64(len(data)) {
		t.Errorf("the image was not extracted beside the sheet: %v", err)
	}

	// Cleanup must actually remove the copy: it is gigabytes on a real rip.
	dir := filepath.Dir(got)
	cleanup()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("scratch directory %s survived cleanup", dir)
	}
}
