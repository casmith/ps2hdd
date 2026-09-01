package external

import (
	"os"
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
)

func readFixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../testdata/hdl_dump/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestParseHDLTocCSV(t *testing.T) {
	toc, err := ParseHDLToc(readFixture(t, "hdl_toc.csv"))
	if err != nil {
		t.Fatalf("ParseHDLToc: %v", err)
	}
	if len(toc.Games) != 4 {
		t.Fatalf("got %d games, want 4", len(toc.Games))
	}
	g := toc.Games[0]
	if g.Startup != "SLUS_210.50" || g.Name != "Burnout 3: Takedown" {
		t.Errorf("game 0 = %+v", g)
	}
	if !g.IsDVD || g.SizeKB != 3538944 {
		t.Errorf("game 0 media/size = %v/%d", g.IsDVD, g.SizeKB)
	}
	if g.CompatFlags != "0" || g.DMA != "*u4" {
		t.Errorf("game 0 flags/dma = %q/%q", g.CompatFlags, g.DMA)
	}
	if toc.Games[1].CompatFlags != "+1+3" {
		t.Errorf("game 1 flags = %q", toc.Games[1].CompatFlags)
	}
	if toc.Games[2].IsDVD {
		t.Error("Ridge Racer V is a CD, not a DVD")
	}
	// Titles containing the field separator must survive intact.
	if got, want := toc.Games[3].Name, "God of War; Special Edition"; got != want {
		t.Errorf("game 3 name = %q, want %q", got, want)
	}
	if toc.Games[3].DMA != "" {
		t.Errorf("game 3 dma = %q, want empty", toc.Games[3].DMA)
	}
	if toc.TotalMB != 114432 || toc.UsedMB != 15104 || toc.FreeMB != 99328 {
		t.Errorf("totals = %d/%d/%d", toc.TotalMB, toc.UsedMB, toc.FreeMB)
	}
}

func TestParseHDLTocIgnoresBannerAndHeader(t *testing.T) {
	// hdl_dump prints its banner and a space-separated header even in CSV
	// mode; neither may be mistaken for a game.
	toc, err := ParseHDLToc(readFixture(t, "hdl_toc.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(toc.Games) != 0 {
		t.Errorf("non-CSV output produced %d games; only --csv output is parsed", len(toc.Games))
	}
	if toc.TotalMB != 114432 {
		t.Errorf("totals should still be read: %d", toc.TotalMB)
	}
}

func TestParseHDLTocEmpty(t *testing.T) {
	toc, err := ParseHDLToc("total 114432MB, used 128MB, available 114304MB\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(toc.Games) != 0 {
		t.Errorf("got %d games on an empty HDD", len(toc.Games))
	}
	if toc.FreeMB != 114304 {
		t.Errorf("free = %d", toc.FreeMB)
	}
}

func TestParseCDVDInfo(t *testing.T) {
	info, err := ParseCDVDInfo(readFixture(t, "cdvd_info2.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if info.MediaType != model.MediaDVD || info.DualLayer {
		t.Errorf("media = %v dual=%v", info.MediaType, info.DualLayer)
	}
	if info.Startup != "SCUS_973.99" || info.VolumeID != "GOD_OF_WAR" {
		t.Errorf("info = %+v", info)
	}
	if info.SizeKB != 4586752 {
		t.Errorf("size = %d", info.SizeKB)
	}

	dl, err := ParseCDVDInfo(readFixture(t, "cdvd_info2_dl.csv"))
	if err != nil {
		t.Fatal(err)
	}
	if !dl.DualLayer || dl.MediaType != model.MediaDVD {
		t.Errorf("dual layer info = %+v", dl)
	}
}

func TestParseCDVDInfoRejectsGarbage(t *testing.T) {
	if _, err := ParseCDVDInfo("no such thing\n"); err == nil {
		t.Fatal("ParseCDVDInfo accepted unrecognisable output")
	}
}

func TestParseHDLProgress(t *testing.T) {
	cases := []struct {
		line string
		want float64
		ok   bool
	}{
		{"[=====>          ]  42%, 00:01:23 remaining, 12.34 MB/sec", 0.42, true},
		{" 7%", 0.07, true},
		{"100%", 1.0, true},
		{"Saving the virtual CD-ROM image. Please wait...", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseHDLProgress(c.line)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("ParseHDLProgress(%q) = %v,%v want %v,%v", c.line, got, ok, c.want, c.ok)
		}
	}
}

func TestInstallArgs(t *testing.T) {
	args, err := InstallArgs(InstallRequest{
		Device: "/dev/disk/by-id/ata-X", Name: "Burnout 3", Source: "/games/b3.iso",
		Startup: "SLUS_210.50", Media: model.MediaDVD, DMA: "*u4",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"inject_dvd", "/dev/disk/by-id/ata-X", "Burnout 3", "/games/b3.iso", "SLUS_210.50", "*u4"}
	if !equal(args, want) {
		t.Errorf("args = %v\nwant %v", args, want)
	}

	cd, err := InstallArgs(InstallRequest{
		Device: "/dev/x", Name: "Ridge Racer V", Source: "/g/rr.iso",
		Startup: "SLUS_200.02", Media: model.MediaCD, CompatFlags: "+1+2", Hidden: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantCD := []string{"inject_cd", "/dev/x", "Ridge Racer V", "/g/rr.iso", "SLUS_200.02", "+1+2", "-hide"}
	if !equal(cd, wantCD) {
		t.Errorf("cd args = %v\nwant %v", cd, wantCD)
	}

	// "0" is how hdl_dump prints "no flags"; passing it back as an argument
	// would be a syntax error, so it must be dropped.
	noFlags, err := InstallArgs(InstallRequest{
		Device: "/dev/x", Name: "G", Source: "/g.iso", Media: model.MediaDVD, CompatFlags: "0",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range noFlags {
		if a == "0" {
			t.Errorf(`compat flag "0" was passed through: %v`, noFlags)
		}
	}
}

func TestInstallArgsRefusesUnknownMedia(t *testing.T) {
	// Choosing between inject_cd and inject_dvd wrongly produces a game the
	// PS2 cannot boot, so an unknown media type is an error rather than a
	// default.
	_, err := InstallArgs(InstallRequest{Device: "/dev/x", Name: "G", Source: "/g.iso"})
	if err == nil {
		t.Fatal("InstallArgs guessed a media type")
	}
}

func TestInstallArgsRequiresFields(t *testing.T) {
	for _, req := range []InstallRequest{
		{Name: "G", Source: "/g.iso", Media: model.MediaDVD},
		{Device: "/dev/x", Source: "/g.iso", Media: model.MediaDVD},
		{Device: "/dev/x", Name: "G", Media: model.MediaDVD},
	} {
		if _, err := InstallArgs(req); err == nil {
			t.Errorf("InstallArgs(%+v) accepted an incomplete request", req)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
