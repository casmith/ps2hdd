package drive

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa/apasynth"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

func TestValidateRejectsKernelDeviceName(t *testing.T) {
	_, err := Validate(context.Background(), "/dev/sdb", Options{})
	if !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if !strings.Contains(err.Error(), "REFUSING OPERATION") {
		t.Errorf("refusal did not announce itself:\n%s", err)
	}
	if !strings.Contains(err.Error(), "stable identifier") {
		t.Errorf("refusal did not explain why:\n%s", err)
	}
}

func TestValidateRejectsRelativePath(t *testing.T) {
	if _, err := Validate(context.Background(), "sdb", Options{}); !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
}

func TestValidateRejectsEmptyDevice(t *testing.T) {
	err := func() error { _, e := Validate(context.Background(), "  ", Options{}); return e }()
	if !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if !strings.Contains(err.Error(), "detect --configure") {
		t.Errorf("refusal is not actionable:\n%s", err)
	}
}

func TestValidateRejectsMissingDevice(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-image.img")
	_, err := Validate(context.Background(), missing, Options{})
	if !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
}

// A disk image is a legitimate target: it is identified by its path, which is
// stable, and it cannot be the system disk.
func TestValidateAcceptsAPAImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ps2.img")
	if err := apasynth.Write(path, apasynth.DefaultDisk()); err != nil {
		t.Fatal(err)
	}
	target, err := Validate(context.Background(), path, Options{RequireAPA: true, Write: true})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !target.IsImage {
		t.Error("IsImage = false")
	}
	if !target.APA {
		t.Error("APA = false")
	}
	if target.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d", target.SizeBytes)
	}
}

func TestValidateRefusesNonAPAWhenRequired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "blank.img")
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Validate(context.Background(), path, Options{RequireAPA: true})
	if !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if !strings.Contains(err.Error(), "never initialises or formats") {
		t.Errorf("refusal should say ps2hdd will not format an unknown disk:\n%s", err)
	}
	// Without RequireAPA the same device validates, but reports APA=false, so
	// that `detect` can describe it rather than refuse outright.
	target, err := Validate(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("Validate without RequireAPA: %v", err)
	}
	if target.APA {
		t.Error("APA = true on a blank image")
	}
}

func TestSameDisk(t *testing.T) {
	cases := []struct {
		src, disk string
		want      bool
	}{
		{"/dev/sda", "/dev/sda", true},
		{"/dev/sda1", "/dev/sda", true},
		{"/dev/sda12", "/dev/sda", true},
		{"/dev/nvme0n1p1", "/dev/nvme0n1", true},
		{"/dev/sdb", "/dev/sda", false},
		{"/dev/sdab", "/dev/sda", false},
		{"/dev/sda", "/dev/sdb", false},
	}
	for _, c := range cases {
		if got := sameDisk(c.src, c.disk); got != c.want {
			t.Errorf("sameDisk(%q, %q) = %v, want %v", c.src, c.disk, got, c.want)
		}
	}
}

func TestParseMountinfoFindsRootDevice(t *testing.T) {
	const sample = `21 26 0:20 / /sys rw,nosuid - sysfs sysfs rw
26 1 8:2 / / rw,relatime - ext4 /dev/sda2 rw
27 26 8:1 / /boot rw,relatime - ext4 /dev/sda1 rw
40 26 0:44 / /run/media/user/My\040Disk rw - exfat /dev/sdc1 rw
`
	mounts, err := parseMountinfo(strings.NewReader(sample))
	if err != nil {
		t.Fatal(err)
	}
	if got := mounts["/dev/sda2"]; len(got) != 1 || got[0] != "/" {
		t.Errorf("/dev/sda2 -> %v, want [/]", got)
	}
	if got := mounts["/dev/sda1"]; len(got) != 1 || got[0] != "/boot" {
		t.Errorf("/dev/sda1 -> %v, want [/boot]", got)
	}
	// Mountpoints with spaces are octal-escaped by the kernel.
	if got := mounts["/dev/sdc1"]; len(got) != 1 || got[0] != "/run/media/user/My Disk" {
		t.Errorf("/dev/sdc1 -> %v, want the unescaped path", got)
	}
	if _, ok := mounts["sysfs"]; ok {
		t.Error("pseudo-filesystems should not be recorded")
	}
}

func TestCheckNotSystemDiskRejectsRoot(t *testing.T) {
	// The real mount table is used here: whatever backs / on this machine must
	// be refused. Finding the device the running system boots from is the
	// point of the check, so exercising it against the real one is the honest
	// test.
	mounts, err := SystemDevices()
	if err != nil {
		t.Skipf("mountinfo unavailable: %v", err)
	}
	var rootDev string
	for src, points := range mounts {
		for _, p := range points {
			if p == "/" {
				rootDev = src
			}
		}
	}
	if rootDev == "" {
		t.Skip("no device-backed root filesystem on this machine")
	}
	err = checkNotSystemDisk("/dev/disk/by-id/fake", rootDev)
	if !IsRefusal(err) {
		t.Fatalf("checkNotSystemDisk(%s) = %v, want a Refusal", rootDev, err)
	}
	if !strings.Contains(err.Error(), "running system") {
		t.Errorf("refusal should name the system disk:\n%s", err)
	}
}

func TestHintedSerial(t *testing.T) {
	cases := map[string]string{
		"ata-WDC_WD1200JB-00REA0_WD-WCANM1234567":       "WD-WCANM1234567",
		"nvme-WD_BLACK_SN770_1TB_22354M800123":          "22354M800123",
		"ata-WDC_WD1200JB-00REA0_WD-WCANM1234567-part1": "WD-WCANM1234567",
		"wwn-0x50014ee2b3c4d5e6":                        "",
		"dm-name-luks-1234":                             "",
	}
	for in, want := range cases {
		if got := hintedSerial(in); got != want {
			t.Errorf("hintedSerial(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModelConsistent(t *testing.T) {
	const link = "ata-WDC_WD1200JB-00REA0_WD-WCANM1234567"
	if !modelConsistent(link, "WDC WD1200JB-00REA0") {
		t.Error("matching model rejected")
	}
	if modelConsistent(link, "Samsung SSD 860 EVO 500GB") {
		t.Error("a completely different model was accepted")
	}
	// wwn links carry no model, so they cannot contradict one.
	if !modelConsistent("wwn-0x50014ee2b3c4d5e6", "Samsung SSD 860 EVO") {
		t.Error("wwn link should not be judged on model")
	}
	// An unknown model reported by the kernel is not evidence of a mismatch.
	if !modelConsistent(link, "") {
		t.Error("empty model rejected")
	}
}

// The system-disk refusal must win over the "permission denied" a raw device
// read produces, because a user told they lack permission will reach for sudo,
// and reaching for sudo on the disk their system is running from is exactly
// what must not happen.
func TestSystemDiskRefusalBeatsPermissionError(t *testing.T) {
	mounts, err := SystemDevices()
	if err != nil {
		t.Skipf("mountinfo unavailable: %v", err)
	}
	var rootDev string
	for src, points := range mounts {
		for _, p := range points {
			if p == "/" {
				rootDev = src
			}
		}
	}
	if rootDev == "" {
		t.Skip("no device-backed root filesystem on this machine")
	}
	// Find a by-id link for the disk carrying root, so the check runs through
	// the same path a configured device would.
	byID := PreferredByID(rootDev)
	if byID == "" {
		// Partitions have their own links; fall back to the parent disk.
		t.Skip("no by-id link resolves to the root device")
	}
	err = func() error { _, e := Validate(context.Background(), byID, Options{}); return e }()
	if !IsRefusal(err) {
		t.Fatalf("err = %v, want a Refusal", err)
	}
	if strings.Contains(err.Error(), "permission denied") {
		t.Errorf("the refusal blamed permissions instead of naming the system disk:\n%s", err)
	}
	if !strings.Contains(err.Error(), "running system") {
		t.Errorf("the refusal did not name the system disk:\n%s", err)
	}
}

// `ps2hdd unmount` must never be a way to unmount something ps2hdd did not
// create. The containment check is the whole safety story for that command.
func TestUnmountPathRefusesOutsideRuntimeRoot(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	m := NewMountManager(external.PFS{Runner: external.NewFakeRunner()}, "/dev/null")

	for _, path := range []string{"/etc", "/", "/home", "/mnt/somewhere"} {
		err := m.UnmountPath(context.Background(), path)
		if err == nil {
			t.Fatalf("UnmountPath(%q) was accepted", path)
		}
		if !strings.Contains(err.Error(), "refusing to unmount") {
			t.Errorf("UnmountPath(%q) failed for the wrong reason: %v", path, err)
		}
	}
}

// A partition ps2hdd mounted for its own work is released on Close, and the
// per-process directory goes with it.
func TestMountManagerReleasesOwnMounts(t *testing.T) {
	runtime := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", runtime)

	backing := t.TempDir()
	if err := os.WriteFile(filepath.Join(backing, "marker"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := external.NewFakeRunner()
	f.Handler = func(c external.Command) (external.Result, error) {
		switch c.Name {
		case external.PFSFuseTool:
			// Stand a symlink in for the FUSE mount.
			mp := c.Args[len(c.Args)-1]
			if err := os.Remove(mp); err != nil && !os.IsNotExist(err) {
				return external.Result{}, err
			}
			return external.Result{}, os.Symlink(backing, mp)
		case external.FusermountTool, external.FusermountLegcy:
			for _, a := range c.Args {
				if a != "-u" {
					_ = os.Remove(a)
				}
			}
		}
		return external.Result{}, nil
	}

	m := NewMountManager(external.PFS{Runner: f}, "/dev/null")
	ctx := context.Background()

	var seen string
	if err := m.With(ctx, "+OPL", func(mp string) error {
		seen = mp
		if _, err := os.Stat(filepath.Join(mp, "marker")); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("With: %v", err)
	}
	if seen == "" {
		t.Fatal("the callback never ran")
	}
	// With releases on the way out.
	if len(m.Owned()) != 0 {
		t.Errorf("owned after With = %v", m.Owned())
	}

	// A nested mount of the same partition is reference counted, not mounted
	// twice, and survives until the outer caller is done.
	mp1, err := m.Mount(ctx, "+OPL")
	if err != nil {
		t.Fatal(err)
	}
	mp2, err := m.Mount(ctx, "+OPL")
	if err != nil {
		t.Fatal(err)
	}
	if mp1 != mp2 {
		t.Errorf("nested mounts gave different paths: %q and %q", mp1, mp2)
	}
	if err := m.Unmount(ctx, "+OPL"); err != nil {
		t.Fatal(err)
	}
	if len(m.Owned()) != 1 {
		t.Error("the inner release unmounted while the outer caller was still using it")
	}
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(mp1); !os.IsNotExist(err) {
		t.Errorf("the mountpoint survived Close: %v", err)
	}
}

// Unmounting a partition this manager never mounted must do nothing, not
// unmount somebody else's mount that happens to share the name.
func TestUnmountUntrackedPartitionIsANoOp(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	f := external.NewFakeRunner()
	m := NewMountManager(external.PFS{Runner: f}, "/dev/null")
	if err := m.Unmount(context.Background(), "+OPL"); err != nil {
		t.Fatalf("Unmount of an untracked partition: %v", err)
	}
	if got := f.CallsTo(external.FusermountTool); len(got) != 0 {
		t.Errorf("an untracked partition triggered %d unmount calls", len(got))
	}
}
