package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

// scratchEnv builds a Services whose scratch root is a temporary directory.
func scratchEnv(t *testing.T) (*Services, string) {
	t.Helper()
	root := t.TempDir()
	s := &Services{Config: config.Config{Install: config.InstallConfig{ScratchDir: root}}}
	return s, root
}

// makeScratch creates a scratch-shaped directory of the given age.
func makeScratch(t *testing.T, root, name string, age time.Duration, size int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if size > 0 {
		if err := os.WriteFile(filepath.Join(dir, "image.iso"), make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(dir, when, when); err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every extraction removes its own directory, but a deferred cleanup does not
// run when the process is killed. What is left has to be reclaimable, and what
// is in use must not be touched: a directory being written to right now is
// minutes old, so age is what separates them.
func TestStaleScratchFindsOnlyAbandonedDirectories(t *testing.T) {
	s, root := scratchEnv(t)
	old := makeScratch(t, root, "extract-old", 48*time.Hour, 1000)
	makeScratch(t, root, "vcd-live", time.Minute, 500)
	makeScratch(t, root, "ps1-recent", 23*time.Hour, 500)
	// Something the user put there is not ps2hdd's to delete.
	makeScratch(t, root, "my-own-files", 72*time.Hour, 100)
	// Nor is a loose file.
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	dirs, bytes, err := s.StaleScratch()
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || dirs[0] != old {
		t.Fatalf("got %v, want just %s", dirs, old)
	}
	if bytes != 1000 {
		t.Errorf("size = %d, want 1000", bytes)
	}
}

func TestReapStaleScratchRemovesOnlyThose(t *testing.T) {
	s, root := scratchEnv(t)
	old := makeScratch(t, root, "extract-old", 48*time.Hour, 10)
	live := makeScratch(t, root, "extract-live", time.Minute, 10)
	mine := makeScratch(t, root, "keep-me", 48*time.Hour, 10)

	n, bytes := s.ReapStaleScratch(context.Background())
	if n != 1 || bytes != 10 {
		t.Errorf("reaped %d dirs / %d bytes, want 1 / 10", n, bytes)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("an abandoned directory survived")
	}
	for _, keep := range []string{live, mine} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed", keep)
		}
	}
}

// No scratch directory at all is no leftovers, not a failure to look. Doctor
// runs this on every invocation, including before anything has been installed.
func TestStaleScratchWithNoDirectory(t *testing.T) {
	s := &Services{Config: config.Config{Install: config.InstallConfig{
		ScratchDir: filepath.Join(t.TempDir(), "never-created"),
	}}}
	dirs, bytes, err := s.StaleScratch()
	if err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if len(dirs) != 0 || bytes != 0 {
		t.Errorf("found %v / %d bytes where there is no directory", dirs, bytes)
	}
	if n, _ := s.ReapStaleScratch(context.Background()); n != 0 {
		t.Errorf("reaped %d from a directory that does not exist", n)
	}
}

// Conversion staging must land in the scratch directory, not the system
// temporary one. /tmp is tmpfs on most distributions and a VCD is the whole
// disc, so converting there puts hundreds of megabytes in RAM -- which is
// exactly what install.scratch_dir exists to let a user avoid.
func TestVCDStagingLivesInScratch(t *testing.T) {
	s, root := scratchEnv(t)
	dir, err := s.vcdStaging(model.Game{
		Title: "Metal Gear Solid",
		Discs: []model.Disc{{SizeBytes: 1000}, {SizeBytes: 2000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") {
		t.Fatalf("staging %s is not inside the scratch root %s", dir, root)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("staging directory was not created: %v", err)
	}
	// It is named like the others so the reaper recognises it.
	if !isScratchDir(filepath.Base(dir)) {
		t.Errorf("%s would not be reclaimed as scratch", filepath.Base(dir))
	}
}

// The room needed is the largest disc, not their total: discs are converted
// and copied one at a time, and demanding the sum would refuse a three-disc
// release on a drive with ample room for it.
func TestVCDStagingSizesForTheLargestDisc(t *testing.T) {
	s, root := scratchEnv(t)
	free, err := freeSpace(root)
	if err != nil {
		t.Skipf("cannot measure free space: %v", err)
	}
	// Three discs that together exceed the free space, none of which alone
	// comes close.
	each := free/2 - scratchHeadroom
	if each <= 0 {
		t.Skip("not enough free space to construct the case")
	}
	g := model.Game{Title: "Big", Discs: []model.Disc{
		{SizeBytes: each}, {SizeBytes: each}, {SizeBytes: each},
	}}
	if _, err := s.vcdStaging(g); err != nil {
		t.Errorf("a title whose largest disc fits was refused: %v", err)
	}
	// One disc bigger than the whole filesystem is refused, before any work.
	huge := model.Game{Title: "Huge", Discs: []model.Disc{{SizeBytes: free * 4}}}
	if _, err := s.vcdStaging(huge); err == nil {
		t.Error("a disc larger than the free space was accepted")
	}
}
