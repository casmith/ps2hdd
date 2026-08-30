package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/demo"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
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
	g, err := svc.InspectSource(iso)
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
	g, err := svc.InspectSources(paths, "Metal Gear Solid")
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

	// POPStarter's disc-swap file must list every disc in order.
	m, err := svc.Mounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = m.With(ctx, "__.POPS", func(mp string) error {
		body, err := os.ReadFile(filepath.Join(mp, "SLUS_005.94.Metal Gear Solid", "DISCS.TXT"))
		if err != nil {
			return err
		}
		lines := strings.Fields(strings.ReplaceAll(string(body), "\n", " "))
		if len(lines) < 2 {
			t.Errorf("DISCS.TXT = %q", body)
		}
		if !strings.Contains(string(body), "_CD1.VCD") || !strings.Contains(string(body), "_CD2.VCD") {
			t.Errorf("DISCS.TXT does not list both discs: %q", body)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("checking DISCS.TXT: %v", err)
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
	g, err := svc.InspectSource(iso)
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
	for _, n := range []string{"POPS.ELF", "IOPRP252.IMG"} {
		if err := os.WriteFile(filepath.Join(importDir, n), []byte("user supplied"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
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
		g, err := svc.InspectSource(filepath.Join(env.PS2Source(), name))
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
