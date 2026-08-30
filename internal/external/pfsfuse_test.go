package external

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestMountArgs(t *testing.T) {
	args, err := MountArgs("/dev/disk/by-id/ata-X", "+OPL", "/run/ps2hdd/opl", false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--partition=+OPL", "/dev/disk/by-id/ata-X", "/run/ps2hdd/opl"}
	if !equal(args, want) {
		t.Errorf("args = %v, want %v", args, want)
	}

	allow, err := MountArgs("/dev/x", "__.POPS", "/mnt", true)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(allow, []string{"--partition=__.POPS", "-o", "allow_other", "/dev/x", "/mnt"}) {
		t.Errorf("allow_other args = %v", allow)
	}

	for _, bad := range [][3]string{{"", "+OPL", "/mnt"}, {"/dev/x", "", "/mnt"}, {"/dev/x", "+OPL", ""}} {
		if _, err := MountArgs(bad[0], bad[1], bad[2], false); err == nil {
			t.Errorf("MountArgs%v accepted an incomplete request", bad)
		}
	}
}

func TestUnmountPrefersFusermount3(t *testing.T) {
	f := NewFakeRunner()
	p := PFS{Runner: f}
	if err := p.Unmount(context.Background(), "/mnt/x"); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Name != FusermountTool {
		t.Fatalf("calls = %+v, want one %s", calls, FusermountTool)
	}
	if !equal(calls[0].Args, []string{"-u", "/mnt/x"}) {
		t.Errorf("args = %v", calls[0].Args)
	}
}

func TestUnmountFallsBackToFusermount(t *testing.T) {
	// Some distributions ship only the fuse2 binary name.
	f := NewFakeRunner()
	f.Missing[FusermountTool] = true
	p := PFS{Runner: f}
	if err := p.Unmount(context.Background(), "/mnt/x"); err != nil {
		t.Fatal(err)
	}
	if got := f.CallsTo(FusermountLegcy); len(got) != 1 {
		t.Fatalf("expected a fallback to %s, calls = %+v", FusermountLegcy, f.Calls())
	}
}

func TestMountReportsToolMissing(t *testing.T) {
	f := NewFakeRunner()
	f.Missing[PFSFuseTool] = true
	p := PFS{Runner: f}
	err := p.Mount(context.Background(), "/dev/x", "+OPL", t.TempDir())
	if !errors.Is(err, ErrToolMissing) {
		t.Fatalf("err = %v, want ErrToolMissing", err)
	}
	if !strings.Contains(err.Error(), "+OPL") {
		t.Errorf("error should name the partition: %v", err)
	}
}

func TestMountRejectsMissingMountpoint(t *testing.T) {
	p := PFS{Runner: NewFakeRunner()}
	if err := p.Mount(context.Background(), "/dev/x", "+OPL", "/nonexistent/mountpoint"); err == nil {
		t.Fatal("Mount accepted a mountpoint that does not exist")
	}
}

func TestParsePFSShellLs(t *testing.T) {
	const out = `pfsshell 1.1.1
> device /dev/sdb
# ls
__mbr
__net
__system
__sysconf
__common
+OPL
__.POPS
PP.HDL.Burnout 3
# exit
`
	got := ParsePFSShellLs(out)
	want := []string{"__mbr", "__net", "__system", "__sysconf", "__common", "+OPL", "__.POPS", "PP.HDL.Burnout"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("partition %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestMkPartScript(t *testing.T) {
	got := MkPartScript("/dev/sdc", "__.POPS", "20G", "PFS")
	want := "device /dev/sdc\nmkpart __.POPS 20G PFS\nexit\n"
	if got != want {
		t.Errorf("MkPartScript = %q, want %q", got, want)
	}
}
