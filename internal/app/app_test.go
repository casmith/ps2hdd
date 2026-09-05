package app_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa/apasynth"
	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/demo"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/titles"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

// newTestServices wires the service layer to a synthetic HDD and source
// library. These are integration tests: they exercise the real install,
// remove, artwork and setup code paths, with only the two external
// executables and the raw block device replaced.
func newTestServices(t *testing.T) (*app.Services, *demo.Env) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	env, err := demo.Setup(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("build the demo environment: %v", err)
	}
	cfg := env.Config(config.Default())
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	svc := app.New(cfg, env.Runner())
	svc.AssumeYes = true
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, env
}

func TestStatusReadsSyntheticDrive(t *testing.T) {
	svc, _ := newTestServices(t)
	st, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.APADetected {
		t.Error("APA not detected")
	}
	if !st.HasOPL || !st.HasPOPS || !st.HasCommon {
		t.Errorf("partitions: OPL=%v POPS=%v common=%v", st.HasOPL, st.HasPOPS, st.HasCommon)
	}
	if st.PS2Games == 0 {
		t.Error("no PS2 games found")
	}
	if st.PS1Games == 0 {
		t.Error("no PS1 games found")
	}
	if st.FreeBytes <= 0 || st.UsedBytes <= 0 || st.TotalBytes <= 0 {
		t.Errorf("storage figures: total=%d used=%d free=%d", st.TotalBytes, st.UsedBytes, st.FreeBytes)
	}
}

func TestInstallAndRemovePS2(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()

	iso := filepath.Join(env.PS2Source(), "Shadow of the Colossus.iso")
	g, err := svc.InspectSource(ctx, iso)
	if err != nil {
		t.Fatalf("InspectSource: %v", err)
	}
	if g.Platform != model.PlatformPS2 || g.GameID != "SCUS_974.72" {
		t.Fatalf("inspected %+v", g)
	}
	if g.Media != model.MediaDVD {
		t.Errorf("media = %q, want dvd for an 800 MB image", g.Media)
	}

	var stages []app.Stage
	rep, err := svc.Install(ctx, g, app.InstallOptions{
		OnProgress: func(p app.Progress) { stages = append(stages, p.Stage) },
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if rep.Game.GameID != "SCUS_974.72" {
		t.Errorf("report = %+v", rep)
	}
	if !containsStage(stages, app.StageInstalling) || !containsStage(stages, app.StageComplete) {
		t.Errorf("stages = %v", stages)
	}

	installed, err := svc.InstalledPS2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := findGame(installed, "SCUS_974.72")
	if found == nil {
		t.Fatalf("the game is not installed: %+v", installed)
	}
	if found.StorageBackend != model.BackendHDL {
		t.Errorf("backend = %q", found.StorageBackend)
	}
	if found.PartitionName == "" {
		t.Error("no partition name recorded")
	}

	// Installing the same title twice must be refused rather than duplicated.
	if _, err := svc.Install(ctx, g, app.InstallOptions{}); !errors.Is(err, app.ErrAlreadyInstalled) {
		t.Errorf("second install returned %v, want ErrAlreadyInstalled", err)
	}

	if _, err := svc.Remove(ctx, *found, app.RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	after, err := svc.InstalledPS2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findGame(after, "SCUS_974.72") != nil {
		t.Error("the game is still installed after removal")
	}
}

func TestInstallMultiDiscPS1(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()

	dir := filepath.Join(env.PS1Source(), "Metal Gear Solid")
	paths := []string{filepath.Join(dir, "Disc 1.cue"), filepath.Join(dir, "Disc 2.cue")}
	g, err := svc.InspectSources(ctx, paths, "Metal Gear Solid")
	if err != nil {
		t.Fatalf("InspectSources: %v", err)
	}
	if g.DiscCount() != 2 {
		t.Fatalf("disc count = %d", g.DiscCount())
	}
	// The discs of a real release carry different serials; that must survive.
	if g.Discs[0].GameID == g.Discs[1].GameID {
		t.Errorf("both discs got serial %q", g.Discs[0].GameID)
	}

	if _, err := svc.Install(ctx, g, app.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	installed, err := svc.InstalledPS1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var mgs *model.Game
	for i := range installed {
		if installed[i].Title == "Metal Gear Solid" {
			mgs = &installed[i]
		}
	}
	if mgs == nil {
		t.Fatalf("Metal Gear Solid is not installed: %+v", installed)
	}
	// It must come back as one logical title with two discs, not two titles.
	if mgs.DiscCount() != 2 {
		t.Errorf("installed as %d discs, want 2: %+v", mgs.DiscCount(), mgs.Discs)
	}
	for i, d := range mgs.Discs {
		if d.Number != i+1 {
			t.Errorf("disc %d has number %d", i, d.Number)
		}
		if !strings.HasSuffix(d.InstalledName, ".VCD") {
			t.Errorf("disc %d installed as %q", d.Number, d.InstalledName)
		}
	}

	// The per-game files live under __common/POPS, one directory per disc, and
	// NOT beside the VCDs in __.POPS. Reading the wrong partition is a mistake
	// with no symptom until a disc change fails mid-game, so the partition is
	// part of what is asserted here.
	m, err := svc.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	discDirs := []string{"SLUS_005.94.Metal Gear Solid_CD1", "SLUS_007.76.Metal Gear Solid_CD2"}
	err = m.With(ctx, "__common", func(mp string) error {
		for i, dir := range discDirs {
			// DISCS.TXT goes in every disc's directory, listing all of them.
			body, err := os.ReadFile(filepath.Join(mp, "POPS", dir, "DISCS.TXT"))
			if err != nil {
				return err
			}
			if !strings.Contains(string(body), "_CD1.VCD") || !strings.Contains(string(body), "_CD2.VCD") {
				t.Errorf("%s/DISCS.TXT does not list both discs: %q", dir, body)
			}
			// VMCDIR.TXT goes in the later discs only, naming disc 1's VCD, so
			// that a save made on disc 1 is there after the swap.
			vmc := filepath.Join(mp, "POPS", dir, "VMCDIR.TXT")
			got, err := os.ReadFile(vmc)
			if i == 0 {
				if err == nil {
					t.Errorf("disc 1 has a VMCDIR.TXT pointing at %q; it owns the card", got)
				}
				continue
			}
			if err != nil {
				return err
			}
			if !strings.Contains(string(got), "_CD1.VCD") {
				t.Errorf("%s/VMCDIR.TXT = %q, want disc 1's VCD", dir, got)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("checking the POPStarter support files: %v", err)
	}

	// Nothing belongs in the support directory's old, wrong home.
	err = m.With(ctx, "__.POPS", func(mp string) error {
		for _, dir := range append(discDirs, "SLUS_005.94.Metal Gear Solid") {
			if _, err := os.Stat(filepath.Join(mp, dir)); err == nil {
				t.Errorf("%s was written into __.POPS, where POPStarter does not look", dir)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Removing the title removes every disc.
	if _, err := svc.Remove(ctx, *mgs, app.RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	after, err := svc.InstalledPS1(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range after {
		if g.Title == "Metal Gear Solid" {
			t.Errorf("discs remain after removing the title: %+v", g.Discs)
		}
	}
}

// A dry run must describe the write without performing it.
func TestDryRunWritesNothing(t *testing.T) {
	svc, env := newTestServices(t)
	svc.DryRun = true
	ctx := context.Background()

	iso := filepath.Join(env.PS2Source(), "Shadow of the Colossus.iso")
	g, err := svc.InspectSource(ctx, iso)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := svc.Install(ctx, g, app.InstallOptions{})
	if err != nil {
		t.Fatalf("dry-run install: %v", err)
	}
	if !rep.DryRun {
		t.Error("the report is not marked as a dry run")
	}
	if len(rep.Commands) == 0 {
		t.Error("a dry run should show the command it would run")
	}
	if !strings.Contains(strings.Join(rep.Commands[0], " "), "inject_dvd") {
		t.Errorf("command = %v", rep.Commands[0])
	}
	installed, err := svc.InstalledPS2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findGame(installed, "SCUS_974.72") != nil {
		t.Error("a dry run installed the game")
	}
}

func TestFindInstalledIsUnambiguous(t *testing.T) {
	svc, _ := newTestServices(t)
	ctx := context.Background()

	g, err := svc.FindInstalled(ctx, "SLUS_210.50")
	if err != nil {
		t.Fatalf("lookup by serial: %v", err)
	}
	if g.Title != "Burnout 3 Takedown" {
		t.Errorf("found %q", g.Title)
	}
	// Loose spellings of the same serial resolve identically.
	if g2, err := svc.FindInstalled(ctx, "slus-21050"); err != nil || g2.GameID != g.GameID {
		t.Errorf("loose serial lookup gave %+v, %v", g2, err)
	}
	if _, err := svc.FindInstalled(ctx, "no such game"); !errors.Is(err, app.ErrNotFound) {
		t.Errorf("missing game error = %v", err)
	}
}

func TestSyncAssetsNeverOverwrites(t *testing.T) {
	svc, _ := newTestServices(t)
	ctx := context.Background()

	// The demo ships Burnout 3 with a cover already in place and a mirror that
	// has covers for the others.
	rowsBefore, err := svc.AssetStatus(ctx, nil)
	if err != nil {
		t.Fatalf("AssetStatus: %v", err)
	}
	if len(rowsBefore) == 0 {
		t.Fatal("no artwork rows")
	}

	m, err := svc.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var originalCover []byte
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		b, err := os.ReadFile(filepath.Join(mp, "ART", "SLUS_210.50_COV.png"))
		originalCover = b
		return err
	})
	if err != nil {
		t.Fatalf("reading the existing cover: %v", err)
	}

	_, res, err := svc.SyncAssets(ctx, nil, app.SyncAssetsOptions{})
	if err != nil {
		t.Fatalf("SyncAssets: %v", err)
	}
	if len(res.Installed) == 0 {
		t.Error("nothing was installed despite missing covers")
	}

	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		b, err := os.ReadFile(filepath.Join(mp, "ART", "SLUS_210.50_COV.png"))
		if err != nil {
			return err
		}
		if string(b) != string(originalCover) {
			t.Error("an existing cover was replaced by a sync that was not asked to overwrite")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPS1ReadinessAndImport(t *testing.T) {
	svc, _ := newTestServices(t)
	ctx := context.Background()

	ready, err := svc.PS1Readiness(ctx)
	if err != nil {
		t.Fatalf("PS1Readiness: %v", err)
	}
	if ready.Ready() {
		t.Fatal("PS1 reported ready with no POPS runtime")
	}
	if len(ready.Missing) != 2 {
		t.Errorf("missing = %v, want the two Sony files", ready.Missing)
	}
	explain := strings.Join(ready.Explain(), "\n")
	if !strings.Contains(explain, "cannot legally distribute") {
		t.Errorf("the explanation does not say why:\n%s", explain)
	}

	// Importing user-supplied runtime files makes it ready, and files that are
	// not part of the runtime are left alone.
	importDir := t.TempDir()
	const placeholder = "user supplied"
	for _, n := range []string{"POPS.ELF", "IOPRP252.IMG"} {
		if err := os.WriteFile(filepath.Join(importDir, n), []byte(placeholder), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The runtime is verified by content, and no test can produce Sony's
	// actual binaries. Point the manifest at the placeholder's hash for the
	// rest of this test so the ready path is still exercised end to end.
	sum := sha256.Sum256([]byte(placeholder))
	defer swapRuntimeHashes(t, hex.EncodeToString(sum[:]))()
	if err := os.WriteFile(filepath.Join(importDir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	rep, err := svc.SetupPS1(ctx, app.SetupPS1Options{ImportDir: importDir})
	if err != nil {
		t.Fatalf("SetupPS1: %v", err)
	}
	if !rep.Readiness.Ready() {
		t.Errorf("still not ready after importing: %+v", rep.Readiness)
	}
	if len(rep.Imported) != 2 {
		t.Errorf("imported %v", rep.Imported)
	}
	if len(rep.Readiness.Wrong) != 0 {
		t.Errorf("files that match their published hash were reported wrong: %v", rep.Readiness.Wrong)
	}
	if len(rep.Ignored) != 1 || rep.Ignored[0] != "notes.txt" {
		t.Errorf("ignored = %v, want just notes.txt", rep.Ignored)
	}
}

func TestCatalogReconcilesBothSides(t *testing.T) {
	svc, _ := newTestServices(t)
	c, warnings, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	for _, w := range warnings {
		t.Logf("warning: %v", w)
	}
	if len(c.Entries) == 0 {
		t.Fatal("empty catalog")
	}
	var bothSides int
	for _, e := range c.Entries {
		if e.Installed && e.AvailableInSource {
			bothSides++
			if e.SourceGame == nil {
				t.Errorf("%s is on both sides but carries no source record", e.Title)
			}
		}
	}
	if bothSides == 0 {
		t.Error("no title was matched between the HDD and the source directories")
	}
}

func TestQueueRunsInstallsInOrder(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()

	var games []model.Game
	for _, name := range []string{"Shadow of the Colossus.iso", "Gran Turismo 4.iso"} {
		g, err := svc.InspectSource(ctx, filepath.Join(env.PS2Source(), name))
		if err != nil {
			t.Fatal(err)
		}
		games = append(games, g)
	}

	q := app.NewQueue(svc, app.InstallOptions{})
	done := make(chan struct{})
	// An item can report completion more than once, and callbacks may run
	// concurrently, so the close is guarded by a Once rather than a
	// check-then-act on the channel.
	var once sync.Once
	var seen []app.QueueState
	var seenMu sync.Mutex
	q.OnUpdate(func(it app.QueueItem) {
		seenMu.Lock()
		if it.ID == 1 {
			seen = append(seen, it.State)
		}
		seenMu.Unlock()
		if it.State == app.QueueComplete && it.ID == 2 {
			once.Do(func() { close(done) })
		}
	})
	q.Add(games...)
	if q.Pending() != 2 {
		t.Fatalf("pending = %d", q.Pending())
	}
	q.Start(ctx)
	<-done

	complete, failed, _ := q.Summary()
	if complete != 2 || failed != 0 {
		t.Errorf("queue finished with %d complete, %d failed: %+v", complete, failed, q.Items())
	}

	// Progress must be delivered in order, and an item must be announced
	// complete exactly once and last. A consumer treats QueueComplete as
	// "reload the library", so a duplicate costs a redundant HDD read.
	seenMu.Lock()
	defer seenMu.Unlock()
	completes := 0
	for i, st := range seen {
		if st != app.QueueComplete {
			continue
		}
		completes++
		if i != len(seen)-1 {
			t.Errorf("item 1 reported %v after completing: %v", seen[i+1:], seen)
		}
	}
	if completes != 1 {
		t.Errorf("item 1 announced complete %d times, want 1: %v", completes, seen)
	}
	installed, err := svc.InstalledPS2(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if findGame(installed, g.GameID) == nil {
			t.Errorf("%s was not installed by the queue", g.GameID)
		}
	}
}

func TestMountsAreReleasedOnClose(t *testing.T) {
	svc, _ := newTestServices(t)
	ctx := context.Background()
	m, err := svc.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mp, err := m.Mount(ctx, drive.PartitionOPL)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Owned()) != 1 {
		t.Errorf("owned = %v", m.Owned())
	}
	if err := svc.Close(ctx); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Lstat(mp); !os.IsNotExist(err) {
		t.Errorf("the mountpoint %s survived Close (err=%v)", mp, err)
	}
}

func containsStage(stages []app.Stage, want app.Stage) bool {
	for _, s := range stages {
		if s == want {
			return true
		}
	}
	return false
}

func findGame(games []model.Game, id string) *model.Game {
	want := model.NormalizeGameID(id)
	for i := range games {
		if model.NormalizeGameID(games[i].GameID) == want {
			return &games[i]
		}
	}
	return nil
}

// A missing optional tool is a setup gap, not a malfunction. It has to be
// reported as such, with the remedy attached, because it recurs on every
// refresh until the user installs the tool -- and a permanent red banner
// teaches people to ignore red banners.
func TestMissingToolIsReportedAsASetupGap(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	env, err := demo.Setup(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := env.Config(config.Default())
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))

	// A runner that has everything except pfsfuse, which is exactly the state
	// of a machine where hdl_dump was installed but pfsshell was not.
	runner := env.Runner().(*external.FakeRunner)
	runner.Missing[external.PFSFuseTool] = true

	svc := app.New(cfg, runner)

	svc.Titles = titles.NewOffline() // tests never reach the network
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	ctx := context.Background()

	// The catalog still loads: PS2 games come from the native APA reader and
	// need no external tool at all.
	c, warnings, err := svc.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	var ps2 int
	for _, e := range c.Entries {
		if e.Installed && e.Platform == model.PlatformPS2 {
			ps2++
		}
	}
	if ps2 == 0 {
		t.Error("no PS2 games listed; the read path should not depend on pfsfuse")
	}
	if len(warnings) == 0 {
		t.Fatal("no warning about the missing tool")
	}

	var found bool
	for _, w := range warnings {
		mt, ok := app.AsMissingTool(w)
		if !ok {
			continue
		}
		found = true
		if mt.Tool != external.PFSFuseTool {
			t.Errorf("Tool = %q", mt.Tool)
		}
		if !strings.Contains(mt.Error(), "not installed") {
			t.Errorf("message does not say what is wrong: %q", mt.Error())
		}
		if !strings.Contains(mt.Advice(), "Install") || !strings.Contains(mt.Advice(), "dependencies.md") {
			t.Errorf("advice is not actionable: %q", mt.Advice())
		}
		if !app.IsSetupGap(w) {
			t.Error("IsSetupGap = false for a missing tool")
		}
	}
	if !found {
		t.Errorf("warnings were not classified as a setup gap: %v", warnings)
	}

	// The artwork paths report the same way rather than leaking a raw
	// "external tool not found" from deep in the mount code.
	if _, err := svc.AssetStatus(ctx, nil); err == nil {
		t.Error("AssetStatus succeeded without pfsfuse")
	} else if _, ok := app.AsMissingTool(err); !ok {
		t.Errorf("AssetStatus error is not a MissingToolError: %v", err)
	}
}

// A library that could not be read is not an empty library.
//
// Reconciling an empty installed set against the source directories produces a
// catalog in which every title is "available", and `install --from-source`
// acts on exactly that judgement. A safety refusal reaches this path the same
// way a corrupt partition table does, so the test drives it with a device that
// cannot be validated at all.
func TestCatalogFailsWhenTheHDDCannotBeRead(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	env, err := demo.Setup(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := env.Config(config.Default())
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	// The source directories stay configured, so a catalog built from them
	// alone would look perfectly healthy. Only the HDD is unreachable.
	cfg.Device = filepath.Join(root, "not-a-disk.img")

	svc := app.New(cfg, env.Runner())
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	c, _, err := svc.Catalog(context.Background())
	if err == nil {
		t.Fatalf("Catalog reported success with an unreadable HDD and returned %d entries", len(c.Entries))
	}
	if len(c.Entries) != 0 {
		t.Errorf("a usable-looking catalog came back with the error: %d entries", len(c.Entries))
	}
}

func TestNormalisePartitionSize(t *testing.T) {
	good := map[string]string{
		"20G": "20G", "8g": "8G", " 1G ": "1G", "128M": "128M", "256m": "256M",
	}
	for in, want := range good {
		got, err := app.NormalisePartitionSize(in)
		if err != nil || got != want {
			t.Errorf("NormalisePartitionSize(%q) = %q, %v; want %q, nil", in, got, err, want)
		}
	}
	// APA allocates in 128 MiB units, so a size that cannot be honoured as
	// written is rejected rather than silently rounded.
	for _, in := range []string{"", "20", "20T", "0G", "-1G", "64M", "200M", "1.5G", "GG"} {
		if got, err := app.NormalisePartitionSize(in); err == nil {
			t.Errorf("NormalisePartitionSize(%q) = %q, want an error", in, got)
		}
	}
}

// pfsshell is a shell: a failed `mkpart` prints "(!) Exit code is -1." and the
// shell then exits 0 anyway. Nothing may conclude the partition exists from
// the tool having run -- only from reading the table back.
func TestCreatePOPSPartitionVerifiesTheResult(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	disk := apasynth.DefaultDisk()
	var without []apasynth.PFSPart
	for _, p := range disk.Parts {
		if p.ID != ps1.POPSPartition {
			without = append(without, p)
		}
	}
	disk.Parts = without

	img := filepath.Join(root, "ps2.img")
	if err := apasynth.Write(img, disk); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	cfg.Device = img

	runner := external.NewFakeRunner()
	runner.Responses[external.PFSShellTool] = []external.Result{{
		Stdout: "> # (!) Exit code is -1.\n__.POPS: not enough free space.\n",
	}}
	svc := app.New(cfg, runner)
	svc.Titles = titles.NewOffline() // tests never reach the network
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	rep, err := svc.CreatePOPSPartition(context.Background(), "8G")
	if err == nil {
		t.Fatal("a pfsshell run that created nothing was reported as success")
	}
	if rep.Created {
		t.Error("report says Created with no partition on the disk")
	}
	// The failure has to carry pfsshell's own words, or the user is left
	// guessing at what went wrong inside a tool they did not run themselves.
	if !strings.Contains(err.Error(), "not enough free space") {
		t.Errorf("error does not quote pfsshell:\n%v", err)
	}
}

func TestCreatePOPSPartitionRefusesWhenItExists(t *testing.T) {
	svc, _ := newTestServices(t)
	runner := svc.Runner.(*external.FakeRunner)

	_, err := svc.CreatePOPSPartition(context.Background(), "8G")
	if err == nil {
		t.Fatal("creating a partition that already exists was allowed")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("err = %v", err)
	}
	// Refused before the tool ran, not after.
	if calls := runner.CallsTo(external.PFSShellTool); len(calls) != 0 {
		t.Errorf("pfsshell was run %d time(s) despite the refusal", len(calls))
	}
}

func TestCreatePOPSPartitionDryRunTouchesNothing(t *testing.T) {
	svc, _ := newTestServices(t)
	svc.DryRun = true
	runner := svc.Runner.(*external.FakeRunner)

	rep, err := svc.CreatePOPSPartition(context.Background(), "8G")
	// The demo disk already has __.POPS, so a dry run must still refuse for
	// that reason; what matters is that no write was attempted either way.
	if err == nil && rep.Created {
		t.Error("a dry run reported the partition as created")
	}
	if calls := runner.CallsTo(external.PFSShellTool); len(calls) != 0 {
		t.Errorf("a dry run ran pfsshell %d time(s)", len(calls))
	}
}

// hdlTocCSV renders rows in the format `hdl_dump hdl_toc --csv` emits, header
// line and all, so the comparison is driven by the real shape of the output.
func hdlTocCSV(rows ...string) string {
	out := "type      size flags       dma startup      name\n"
	for _, r := range rows {
		out += r + "\n"
	}
	return out + "total 114432MB, used 8320MB, available 106112MB\n"
}

// The three games on the synthetic drive, as hdl_dump would report them.
var demoTocRows = []string{
	"DVD;3538944KB;  0        ;*u4;SLUS_210.50;Burnout 3 Takedown",
	"DVD;3014656KB;  0        ;*u4;SLUS_215.03;God Hand",
	"CD;655360KB;  0        ;*u4;SLUS_200.02;Ridge Racer V",
}

// newCrossCheckServices wires a synthetic APA image to a bare FakeRunner.
//
// The demo environment cannot be used here: its runner installs a Handler,
// which FakeRunner consults ahead of any canned Responses, so hdl_dump's
// output could not be controlled per test.
func newCrossCheckServices(t *testing.T) (*app.Services, *external.FakeRunner) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	img := filepath.Join(root, "ps2.img")
	if err := apasynth.Write(img, apasynth.DefaultDisk()); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	cfg.Device = img

	runner := external.NewFakeRunner()
	svc := app.New(cfg, runner)
	svc.Titles = titles.NewOffline() // tests never reach the network
	t.Cleanup(func() { _ = svc.Close(context.Background()) })
	return svc, runner
}

func TestCrossCheckAgrees(t *testing.T) {
	svc, runner := newCrossCheckServices(t)
	runner.Responses[external.HDLDumpTool] = []external.Result{{Stdout: hdlTocCSV(demoTocRows...)}}

	cc, err := svc.CrossCheckReader(context.Background())
	if err != nil {
		t.Fatalf("CrossCheckReader: %v", err)
	}
	if !cc.Agree() {
		t.Fatalf("readers disagree: %v (native=%d ref=%d)", cc.Disagreements, cc.NativeGames, cc.ReferenceGames)
	}
	if cc.NativeGames != 3 || cc.ReferenceGames != 3 {
		t.Errorf("counts: native=%d reference=%d, want 3 and 3", cc.NativeGames, cc.ReferenceGames)
	}
}

func TestCrossCheckReportsEveryKindOfDisagreement(t *testing.T) {
	cases := map[string]struct {
		rows []string
		want string
	}{
		"missing from ps2hdd": {
			rows: append(append([]string{}, demoTocRows...),
				"DVD;1048576KB;  0        ;*u4;SLUS_209.46;Shadow of the Colossus"),
			want: "missing from ps2hdd's",
		},
		"missing from hdl_dump": {
			rows: demoTocRows[:2],
			want: "missing from hdl_dump's",
		},
		"title differs": {
			rows: []string{
				"DVD;3538944KB;  0        ;*u4;SLUS_210.50;Burnout 3",
				demoTocRows[1], demoTocRows[2],
			},
			want: "reads the title as",
		},
		"media differs": {
			rows: []string{
				demoTocRows[0], demoTocRows[1],
				"DVD;655360KB;  0        ;*u4;SLUS_200.02;Ridge Racer V",
			},
			want: "reads the media as CD, hdl_dump as DVD",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			svc, runner := newCrossCheckServices(t)
			runner.Responses[external.HDLDumpTool] = []external.Result{{Stdout: hdlTocCSV(tc.rows...)}}

			cc, err := svc.CrossCheckReader(context.Background())
			if err != nil {
				t.Fatalf("CrossCheckReader: %v", err)
			}
			if cc.Agree() {
				t.Fatal("a disagreement was reported as agreement")
			}
			if !strings.Contains(strings.Join(cc.Disagreements, "\n"), tc.want) {
				t.Errorf("disagreements do not mention %q:\n%s", tc.want, strings.Join(cc.Disagreements, "\n"))
			}
		})
	}
}

// Without hdl_dump the comparison cannot run. That is not a fault in the
// library and must not be reported as agreement either.
func TestCrossCheckWithoutHDLDumpIsNotAgreement(t *testing.T) {
	svc, runner := newCrossCheckServices(t)
	runner.Missing[external.HDLDumpTool] = true

	cc, err := svc.CrossCheckReader(context.Background())
	if err != nil {
		t.Fatalf("CrossCheckReader: %v", err)
	}
	if cc.Ran {
		t.Error("Ran is true with no hdl_dump installed")
	}
	if cc.Agree() {
		t.Error("an unrun cross-check reported agreement")
	}
	if cc.Unavailable == "" {
		t.Error("no reason given for the check not running")
	}
}

// hdl_toc needs raw block access, so an unprivileged run fails while the
// native read succeeds. The library is fine; the comparison is unavailable.
func TestCrossCheckSurvivesAnHDLDumpFailure(t *testing.T) {
	svc, runner := newCrossCheckServices(t)
	runner.Errors[external.HDLDumpTool] = []error{errors.New("Permission denied")}

	cc, err := svc.CrossCheckReader(context.Background())
	if err != nil {
		t.Fatalf("CrossCheckReader returned a hard error: %v", err)
	}
	if cc.Ran || cc.Agree() {
		t.Error("a failed hdl_dump run was treated as a completed comparison")
	}
	if !strings.Contains(cc.Unavailable, "Permission denied") {
		t.Errorf("reason does not quote hdl_dump: %q", cc.Unavailable)
	}
}

// No drive configured is a different thing from a drive that could not be
// read. There is no disk to be wrong about, so the source half must still be
// browsable -- that is the read-only browser the README promises on a machine
// with nothing set up yet.
func TestCatalogWithoutADeviceStillListsSources(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	env, err := demo.Setup(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatal(err)
	}
	cfg := env.Config(config.Default())
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	cfg.Device = "" // sources configured, no drive

	svc := app.New(cfg, env.Runner())
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	c, warnings, err := svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog refused to build without a device: %v", err)
	}
	if len(c.Entries) == 0 {
		t.Fatal("no source titles listed")
	}
	for _, e := range c.Entries {
		if e.Installed {
			t.Errorf("%s is marked installed with no drive configured", e.GameID)
		}
	}
	var said bool
	for _, w := range warnings {
		if errors.Is(w, app.ErrNoDevice) {
			said = true
		}
	}
	if !said {
		t.Errorf("no warning that there is no device: %v", warnings)
	}
}

// swapRuntimeHashes points every hashed runtime file at want for the duration
// of a test, and returns the restore. It exists because the runtime is checked
// by content and a test cannot hold Sony's binaries; the alternative is not
// exercising the ready path at all.
func swapRuntimeHashes(t *testing.T, want string) func() {
	t.Helper()
	saved := make([]string, len(ps1.RuntimeFiles))
	for i := range ps1.RuntimeFiles {
		saved[i] = ps1.RuntimeFiles[i].SHA256
		if ps1.RuntimeFiles[i].SHA256 != "" {
			ps1.RuntimeFiles[i].SHA256 = want
		}
	}
	return func() {
		for i := range ps1.RuntimeFiles {
			ps1.RuntimeFiles[i].SHA256 = saved[i]
		}
	}
}

// A runtime file that is present but is not the file it should be must be
// reported. This is the failure that cost an entire debugging session: a
// POPS.ELF that was a different file altogether, with the drive reporting
// PS1 READY, and every single title black-screening identically because the
// emulator was not the emulator.
func TestReadinessRejectsAWrongRuntimeFile(t *testing.T) {
	ctx := context.Background()
	svc, _ := newTestServices(t)

	importDir := t.TempDir()
	for _, n := range []string{"POPS.ELF", "IOPRP252.IMG"} {
		if err := os.WriteFile(filepath.Join(importDir, n), []byte("not the real thing"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Only POPS.ELF is given the hash it actually has, so IOPRP252.IMG is the
	// odd one out and the check has to single it out rather than condemn both.
	sum := sha256.Sum256([]byte("not the real thing"))
	restore := swapRuntimeHashes(t, hex.EncodeToString(sum[:]))
	defer restore()
	for i := range ps1.RuntimeFiles {
		if ps1.RuntimeFiles[i].Name == "IOPRP252.IMG" {
			ps1.RuntimeFiles[i].SHA256 = strings.Repeat("00", 32)
		}
	}

	rep, err := svc.SetupPS1(ctx, app.SetupPS1Options{ImportDir: importDir})
	if err != nil {
		t.Fatalf("SetupPS1: %v", err)
	}
	if rep.Readiness.Ready() {
		t.Error("a drive whose IOPRP252.IMG is the wrong file reported READY")
	}
	if len(rep.Readiness.Wrong) != 1 || rep.Readiness.Wrong[0] != "IOPRP252.IMG" {
		t.Fatalf("wrong = %v, want just IOPRP252.IMG", rep.Readiness.Wrong)
	}
	if len(rep.Readiness.Missing) != 0 {
		t.Errorf("a wrong file was also reported missing: %v", rep.Readiness.Missing)
	}
	explain := strings.Join(rep.Readiness.Explain(), "\n")
	if !strings.Contains(explain, "not the right file") {
		t.Errorf("the explanation does not say the file is wrong:\n%s", explain)
	}
	if !strings.Contains(explain, "every PS1 title fails") && !strings.Contains(explain, "Every PS1 title fails") {
		t.Errorf("the explanation does not say what the consequence is:\n%s", explain)
	}
}

// stubTitles is a title lookup that answers from a map and never uses a
// network.
type stubTitles struct{ byName map[string]string }

func (s stubTitles) Do(req *http.Request) (*http.Response, error) {
	for serial, title := range s.byName {
		if strings.Contains(req.URL.String(), serial) {
			return &http.Response{StatusCode: 200,
				Body: io.NopCloser(strings.NewReader("Title=" + title + "\n"))}, nil
		}
	}
	return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
}

// A rip called "Disc 1" or "hot-shots-golf-u-scus-94188" should not become the
// name of the game on the console. The serial inside the image should.
func TestInstallNamesAPS1TitleFromItsSerial(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()
	svc.Titles = titles.Open(t.TempDir())
	svc.Titles.HTTP = stubTitles{byName: map[string]string{
		"SLUS_005.94": "Metal Gear Solid",
	}}

	g := ps1SourceGame(t, svc, ctx, env)
	if _, err := svc.Install(ctx, g, app.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	names := popsContents(t, svc, ctx)
	var found string
	for _, n := range names {
		if strings.Contains(n, "Metal Gear Solid") {
			found = n
		}
	}
	if found == "" {
		t.Fatalf("no VCD named from the serial's real title; %s holds %v", ps1.POPSPartition, names)
	}
	if !strings.HasPrefix(found, "SLUS_005.94.") {
		t.Errorf("%q does not keep the serial prefix that OPL artwork is keyed on", found)
	}
}

// An explicit --title is the user's decision and outranks the database.
func TestInstallTitleOverrideBeatsTheDatabase(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()
	svc.Titles = titles.Open(t.TempDir())
	svc.Titles.HTTP = stubTitles{byName: map[string]string{
		"SLUS_005.94": "Metal Gear Solid",
	}}

	g := ps1SourceGame(t, svc, ctx, env)
	if _, err := svc.Install(ctx, g, app.InstallOptions{Title: "My Name For It"}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	names := popsContents(t, svc, ctx)
	for _, n := range names {
		if strings.Contains(n, "My Name For It") {
			return
		}
	}
	t.Errorf("--title was ignored; %s holds %v", ps1.POPSPartition, names)
}

// With the lookup turned off, nothing reaches the network and the filename is
// used exactly as before.
func TestInstallFallsBackToTheFilenameTitle(t *testing.T) {
	svc, env := newTestServices(t)
	ctx := context.Background()
	cfg := svc.Config
	cfg.Install.CanonicalTitles = false
	svc.Config = cfg
	svc.Titles = titles.Open(t.TempDir())
	svc.Titles.HTTP = stubTitles{byName: map[string]string{
		"SLUS_005.94": "Metal Gear Solid",
	}}

	g := ps1SourceGame(t, svc, ctx, env)
	want := g.Title
	if _, err := svc.Install(ctx, g, app.InstallOptions{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, n := range popsContents(t, svc, ctx) {
		if strings.Contains(n, want) {
			return
		}
	}
	t.Errorf("the filename title %q was not used with canonical titles off", want)
}

// ps1SourceGame inspects one PS1 disc straight from the source directory.
//
// Its file is called "Disc 1.cue", which says nothing, so the title that comes
// back is the serial -- exactly the case a lookup is for.
func ps1SourceGame(t *testing.T, svc *app.Services, ctx context.Context, env *demo.Env) model.Game {
	t.Helper()
	g, err := svc.InspectSource(ctx, filepath.Join(env.PS1Source(), "Metal Gear Solid", "Disc 1.cue"))
	if err != nil {
		t.Fatalf("inspect the PS1 source: %v", err)
	}
	if g.Title != "SLUS_005.94" {
		t.Fatalf("fixture changed: the filename-derived title is %q, so this test no longer shows anything", g.Title)
	}
	return g
}

// popsContents lists the VCDs installed in __.POPS.
func popsContents(t *testing.T, svc *app.Services, ctx context.Context) []string {
	t.Helper()
	m, err := svc.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if err := m.With(ctx, ps1.POPSPartition, func(mp string) error {
		entries, err := os.ReadDir(mp)
		if err != nil {
			return err
		}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return nil
	}); err != nil {
		t.Fatalf("read %s: %v", ps1.POPSPartition, err)
	}
	return names
}
