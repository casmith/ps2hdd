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

func TestParseSevenZipList(t *testing.T) {
	// Real `7z l -slt` output, trimmed to the parts that matter.
	const out = `
Listing archive: Devil May Cry (USA).7z

--
Path = Devil May Cry (USA).7z
Type = 7z

----------
Path = Devil May Cry (USA).iso
Size = 4698767360
Packed Size = 1956058463
Attributes = A
CRC = 01E8EAC9

Path = extras
Size = 0
Attributes = D_ drwxr-xr-x

Path = extras/readme.txt
Size = 42
Attributes = A

`
	got := ParseSevenZipList(out)
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2 (the directory must not be one): %+v", len(got), got)
	}
	if got[0].Name != "Devil May Cry (USA).iso" || got[0].SizeBytes != 4698767360 {
		t.Errorf("entry 0 = %+v", got[0])
	}
	if got[1].Name != "extras/readme.txt" || got[1].SizeBytes != 42 {
		t.Errorf("entry 1 = %+v", got[1])
	}
}

func TestIsArchive(t *testing.T) {
	for _, ok := range []string{"a.7z", "A.ZIP", "b.rar", "/path/to/Game (USA).7z"} {
		if !IsArchive(ok) {
			t.Errorf("IsArchive(%q) = false", ok)
		}
	}
	// A Synology share writes a sidecar beside every file; opening one because
	// its name contains ".7z" would be a scan full of spurious errors.
	for _, no := range []string{"a.iso", "Game (USA).7z@SynoEAStream", "b", "c.7z.part"} {
		if IsArchive(no) {
			t.Errorf("IsArchive(%q) = true", no)
		}
	}
}

func TestArchiveArgs(t *testing.T) {
	if got := strings.Join(ListArgs("/x/a.7z"), " "); got != "l -slt /x/a.7z" {
		t.Errorf("ListArgs = %q", got)
	}
	if got := strings.Join(StreamArgs("/x/a.7z", "a.iso"), " "); got != "e -so -spd /x/a.7z a.iso" {
		t.Errorf("StreamArgs = %q", got)
	}
	// -y matters: without it 7z waits forever on an overwrite prompt that no
	// stdin is attached to.
	if got := strings.Join(ExtractArgs("/x/a.7z", "a.iso", "/scratch"), " "); got != "e -y -spd -o/scratch /x/a.7z a.iso" {
		t.Errorf("ExtractArgs = %q", got)
	}
}

// The error a user sees must be what went wrong, not a copyright notice.
// 7-Zip prints four lines of preamble before "ERRORS:" and the real message.
func TestToolErrorSurfacesTheRealMessage(t *testing.T) {
	const sevenZipOutput = `7-Zip 26.02 (x64) : Copyright (c) 1999-2026 Igor Pavlov : 2026-06-25
 64-bit locale=en_US.UTF-8 Threads:16 OPEN_MAX:524288, ASM
Scanning the drive for archives:
1 file, 53075149 bytes (51 MiB)
Listing archive: /games/Assault Rigs.rar
ERRORS:
Unexpected end of archive
WARNINGS:
There are data after the end of archive
`
	err := &ToolError{Tool: "7z", Err: errors.New("exit status 2"), Stdout: sevenZipOutput}
	got := err.Error()
	if !strings.Contains(got, "Unexpected end of archive") {
		t.Errorf("error does not carry 7z's message:\n%s", got)
	}
	if strings.Contains(got, "Copyright") {
		t.Errorf("error leads with the banner:\n%s", got)
	}

	// A tool with no heading still gets its first real line through.
	plain := &ToolError{Tool: "pfsfuse", Err: errors.New("exit status 1"),
		Stderr: "hdd: PS2 APA Driver v2.5 (c) 2003 Vector\n(!) hdd0:+OPL: No such file or directory.\n"}
	if !strings.Contains(plain.Error(), "No such file or directory") {
		t.Errorf("error does not carry pfsfuse's message:\n%s", plain.Error())
	}
}
