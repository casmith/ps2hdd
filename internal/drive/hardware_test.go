//go:build hardware

// Package drive's hardware tests.
//
// These run only with -tags=hardware and only when PS2HDD_TEST_DEVICE names a
// device. They are strictly read-only: they open the device O_RDONLY, parse
// what is there, and assert that the native reader agrees with itself and with
// the drive's own structure.
//
// No test in this repository writes to a block device. Write coverage is the
// manual checklist in docs/hardware-validation.md, because a test that writes
// to a disk is a test that can destroy one.
//
//	PS2HDD_TEST_DEVICE=/dev/disk/by-id/ata-YOUR_DRIVE go test -tags=hardware ./internal/drive/
package drive

import (
	"context"
	"os"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/external"
)

func hardwareDevice(t *testing.T) string {
	t.Helper()
	dev := os.Getenv("PS2HDD_TEST_DEVICE")
	if dev == "" {
		t.Skip("set PS2HDD_TEST_DEVICE to a /dev/disk/by-id path to run the hardware tests")
	}
	return dev
}

func TestHardwareValidateReadOnly(t *testing.T) {
	dev := hardwareDevice(t)
	target, err := Validate(context.Background(), dev, Options{
		Runner:     &external.ExecRunner{},
		RequireAPA: true,
	})
	if err != nil {
		t.Fatalf("Validate(%s): %v", dev, err)
	}
	if !target.APA {
		t.Fatal("the device has no APA table")
	}
	if target.SizeBytes <= 0 {
		t.Fatalf("capacity = %d", target.SizeBytes)
	}
	t.Logf("device %s -> %s, %d bytes, model %q serial %q",
		target.Configured, target.Path, target.SizeBytes, target.Model, target.Serial)
}

func TestHardwareReadTOC(t *testing.T) {
	dev := hardwareDevice(t)
	target, err := Validate(context.Background(), dev, Options{
		Runner: &external.ExecRunner{}, RequireAPA: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	toc, err := apa.ReadTOC(f, target.SizeBytes)
	if err != nil {
		t.Fatalf("ReadTOC: %v", err)
	}
	parts := toc.Partitions()
	if len(parts) == 0 {
		t.Fatal("no partitions")
	}
	// __mbr is always the first partition of a PS2 HDD.
	if _, _, ok := toc.Find("__mbr"); !ok {
		t.Error("no __mbr partition; this may not be a PS2 HDD")
	}
	total, used, free := toc.Chunks()
	if used+free != total {
		t.Errorf("chunk accounting: %d used + %d free != %d total", used, free, total)
	}
	for _, p := range parts {
		t.Logf("%-32s type=%#04x %6d MB", p.ID, p.Type, p.TotalSectors()*apa.SectorSize/(1024*1024))
	}
}

// The native reader and hdl_dump must agree about the installed game list. A
// disagreement means the parser is wrong, and no write should follow.
func TestHardwareGameListMatchesHDLDump(t *testing.T) {
	dev := hardwareDevice(t)
	runner := &external.ExecRunner{}
	if _, err := runner.Look(external.HDLDumpTool); err != nil {
		t.Skipf("hdl_dump is not installed: %v", err)
	}
	target, err := Validate(context.Background(), dev, Options{Runner: runner, RequireAPA: true})
	if err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(target.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, target.SizeBytes)
	if err != nil {
		t.Fatal(err)
	}
	native, err := apa.ReadGames(f, toc)
	if err != nil {
		t.Fatal(err)
	}

	hdl := external.HDLDump{Runner: runner}
	ref, err := hdl.ListGames(context.Background(), target.Path)
	if err != nil {
		t.Fatalf("hdl_dump hdl_toc: %v", err)
	}

	if len(native) != len(ref.Games) {
		t.Fatalf("native reader found %d games, hdl_dump found %d", len(native), len(ref.Games))
	}
	byStartup := map[string]external.HDLGame{}
	for _, g := range ref.Games {
		byStartup[g.Startup] = g
	}
	for _, g := range native {
		want, ok := byStartup[g.Startup]
		if !ok {
			t.Errorf("hdl_dump does not list %s (%s)", g.Startup, g.Name)
			continue
		}
		if g.Name != want.Name {
			t.Errorf("%s: native name %q, hdl_dump %q", g.Startup, g.Name, want.Name)
		}
		if g.IsDVD != want.IsDVD {
			t.Errorf("%s: native IsDVD=%v, hdl_dump=%v", g.Startup, g.IsDVD, want.IsDVD)
		}
		if int64(g.RawSizeKB) != want.SizeKB {
			t.Errorf("%s: native size %d KB, hdl_dump %d KB", g.Startup, g.RawSizeKB, want.SizeKB)
		}
		if g.CompatFlagList() != want.CompatFlags {
			t.Errorf("%s: native flags %q, hdl_dump %q", g.Startup, g.CompatFlagList(), want.CompatFlags)
		}
	}
}

// Detection must find the configured drive and must not misclassify any other
// disk on the machine.
func TestHardwareDetectFindsTheDrive(t *testing.T) {
	dev := hardwareDevice(t)
	candidates, err := Detect(context.Background(), &external.ExecRunner{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range candidates {
		t.Logf("%-16s apa=%v skipped=%q read_error=%q", c.Device.Path, c.APA, c.Skipped, c.ReadError)
		if c.ByID == dev && c.IsCandidate() {
			found = true
		}
	}
	if !found {
		t.Errorf("detect did not report %s as a candidate", dev)
	}
}
