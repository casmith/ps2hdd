package drive

import (
	"os"
	"testing"
)

func TestParseLsblkNumericColumns(t *testing.T) {
	data, err := os.ReadFile("../../testdata/lsblk/workstation.json")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := ParseLsblk(data)
	if err != nil {
		t.Fatalf("ParseLsblk: %v", err)
	}
	if len(devs) != 3 {
		t.Fatalf("got %d devices, want 3", len(devs))
	}

	sda := devs[0]
	if sda.Path != "/dev/sda" || sda.Type != "disk" {
		t.Errorf("sda = %+v", sda)
	}
	if sda.SizeBytes != 512110190592 {
		t.Errorf("sda size = %d", sda.SizeBytes)
	}
	if sda.Serial != "S3Z2NB0K123456A" {
		t.Errorf("sda serial = %q", sda.Serial)
	}
	// The system disk is only recognisable as such through its partitions'
	// mountpoints, which is why children have to be parsed.
	if got := sda.AnyMountpoint(); got == "" {
		t.Error("sda reported no mountpoint despite / and /boot living on it")
	}
	mps := sda.Mountpoints()
	if len(mps) != 2 {
		t.Errorf("sda mountpoints = %v, want /boot and /", mps)
	}

	sdb := devs[1]
	if sdb.AnyMountpoint() != "" {
		t.Errorf("sdb reported mountpoint %q", sdb.AnyMountpoint())
	}
	if sdb.Transport != "sata" {
		t.Errorf("sdb transport = %q", sdb.Transport)
	}

	if !devs[2].ReadOnly {
		t.Error("sr0 should be read-only")
	}
}

func TestParseLsblkStringColumns(t *testing.T) {
	// Older lsblk builds emit every column as a string, including ro and size.
	data, err := os.ReadFile("../../testdata/lsblk/strings.json")
	if err != nil {
		t.Fatal(err)
	}
	devs, err := ParseLsblk(data)
	if err != nil {
		t.Fatalf("ParseLsblk: %v", err)
	}
	if len(devs) != 1 {
		t.Fatalf("got %d devices", len(devs))
	}
	if devs[0].SizeBytes != 1000204886016 {
		t.Errorf("size = %d", devs[0].SizeBytes)
	}
	if devs[0].ReadOnly {
		t.Error(`ro "0" parsed as true`)
	}
	if devs[0].AnyMountpoint() != "/boot/efi" {
		t.Errorf("mountpoint = %q", devs[0].AnyMountpoint())
	}
}

func TestParseLsblkRejectsGarbage(t *testing.T) {
	if _, err := ParseLsblk([]byte("not json")); err == nil {
		t.Fatal("ParseLsblk accepted non-JSON input")
	}
}
