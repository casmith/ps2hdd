package iso9660_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/casmith/ps2hdd/internal/iso9660"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
)

func TestOpenAndReadPS2ISO(t *testing.T) {
	data, err := isosynth.Build(isosynth.Image{
		VolumeID: "SLUS_209.46",
		Files: map[string][]byte{
			"SYSTEM.CNF":  isosynth.PS2SystemCNF("SLUS_209.46"),
			"SLUS_209.46": []byte("fake elf"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := iso9660.Open(iso9660.Mode2048(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if v.VolumeID != "SLUS_209.46" {
		t.Errorf("VolumeID = %q", v.VolumeID)
	}
	if v.SizeBytes() != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", v.SizeBytes(), len(data))
	}
	got, err := v.ReadFile("SYSTEM.CNF")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !bytes.Contains(got, []byte("BOOT2 = cdrom0:\\SLUS_209.46;1")) {
		t.Errorf("SYSTEM.CNF = %q", got)
	}
	names, err := v.ReadDir()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 {
		t.Errorf("ReadDir = %v, want 2 entries", names)
	}
	if _, err := v.ReadFile("NOPE.TXT"); !errors.Is(err, iso9660.ErrNotFound) {
		t.Errorf("missing file error = %v, want ErrNotFound", err)
	}
}

func TestOpenMode2352(t *testing.T) {
	// A PS1 BIN is MODE2/2352: the same ISO filesystem, but each 2048-byte
	// block sits 24 bytes into a 2352-byte sector.
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "SLUS_005.94",
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF("SLUS_005.94")},
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := iso9660.Open(iso9660.Mode2352(bytes.NewReader(data)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := v.ReadFile("SYSTEM.CNF")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("BOOT = cdrom:\\SLUS_005.94;1")) {
		t.Errorf("SYSTEM.CNF = %q", got)
	}
	// Reading a MODE2/2352 image as if it were 2048-byte sectors must fail
	// rather than return nonsense.
	if _, err := iso9660.Open(iso9660.Mode2048(bytes.NewReader(data))); err == nil {
		t.Error("Open accepted a MODE2/2352 image as a 2048-byte image")
	}
}

func TestOpenRejectsNonISO(t *testing.T) {
	if _, err := iso9660.Open(iso9660.Mode2048(bytes.NewReader(make([]byte, 64*2048)))); err == nil {
		t.Fatal("Open accepted a blank image")
	}
}
