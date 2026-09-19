package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/homebrew"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// AppStatus reports, for every app ps2hdd knows about, what is on the drive and
// what upstream currently offers.
//
// A failure to reach upstream is attached to the app it concerns rather than
// failing the whole listing: knowing what is installed is useful with no
// network at all, and it is the half that cannot be looked up again later.
func (s *Services) AppStatus(ctx context.Context) ([]homebrew.Status, error) {
	m, err := s.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	var manifest homebrew.Manifest
	present := map[string]bool{}
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		manifest = homebrew.ReadManifest(mp)
		for _, a := range homebrew.Catalog {
			fi, err := os.Stat(filepath.Join(mp, ps1.AppsDir, a.ID, a.ELFName))
			present[a.ID] = err == nil && fi.Size() > 0
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]homebrew.Status, 0, len(homebrew.Catalog))
	for _, a := range homebrew.Catalog {
		st := homebrew.Status{App: a, Installed: manifest.Apps[a.ID].Version, Present: present[a.ID]}
		st.Latest, st.Err = homebrew.Latest(ctx, s.httpClient(), a)
		out = append(out, st)
	}
	return out, nil
}

// httpClient is the client used for homebrew downloads.
func (s *Services) httpClient() homebrew.Doer {
	if s.HTTP != nil {
		return s.HTTP
	}
	return &http.Client{Timeout: 60 * time.Second}
}

// AppUpdateReport describes one install.
type AppUpdateReport struct {
	App     homebrew.App
	From    string
	To      string
	Files   []string
	Skipped bool
	DryRun  bool
}

// UpdateApp installs the current build of one app into +OPL/APPS.
//
// It writes an app entry, exactly like the launchers PS1 titles get: a
// directory holding the ELF and a title.cfg naming it. That is the one place
// OPL lists programs from, and it is not the boot path -- installing a newer
// OPL here means it can be launched from the current one and tried, which is
// the order worth doing it in.
func (s *Services) UpdateApp(ctx context.Context, id string, force bool) (AppUpdateReport, error) {
	a, ok := homebrew.Find(id)
	if !ok {
		return AppUpdateReport{}, fmt.Errorf("%q is not an app ps2hdd knows about", id)
	}
	rep := AppUpdateReport{App: a, DryRun: s.DryRun}

	rel, err := homebrew.Latest(ctx, s.httpClient(), a)
	if err != nil {
		return rep, err
	}
	rep.To = rel.Version

	m, err := s.Mounts(ctx)
	if err != nil {
		return rep, err
	}
	dir := drive.PartitionOPL + "/" + ps1.AppsDir + "/" + a.ID
	rep.Files = []string{dir + "/" + a.ELFName, dir + "/" + ps1.TitleConfigFile}

	var manifest homebrew.Manifest
	if err := m.With(ctx, drive.PartitionOPL, func(mp string) error {
		manifest = homebrew.ReadManifest(mp)
		return nil
	}); err != nil {
		return rep, err
	}
	rep.From = manifest.Apps[a.ID].Version
	if rep.From == rel.Version && !force {
		rep.Skipped = true
		return rep, nil
	}
	if s.DryRun {
		return rep, nil
	}

	// The download lands in scratch first. A half-written ELF in +OPL/APPS is
	// an app entry that exists and does not run, and the console is a poor
	// place to discover that.
	staged, cleanup, err := s.downloadTo(ctx, rel.URL, a.ELFName)
	if err != nil {
		return rep, err
	}
	defer cleanup()
	if rel.Size > 0 {
		fi, err := os.Stat(staged)
		if err != nil {
			return rep, err
		}
		if fi.Size() != rel.Size {
			return rep, fmt.Errorf("downloaded %s is %d bytes, but the release says %d",
				a.ELFName, fi.Size(), rel.Size)
		}
	}

	unlock := s.LockHDD()
	defer unlock()
	if _, err := s.Target(ctx, true); err != nil {
		return rep, err
	}
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		appDir := filepath.Join(mp, ps1.AppsDir, a.ID)
		if err := os.MkdirAll(appDir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := copyFile(staged, filepath.Join(appDir, a.ELFName)); err != nil {
			return fmt.Errorf("copy %s: %w", a.ELFName, err)
		}
		cfg := ps1.TitleConfigContents(a.Name, a.ELFName)
		if err := writeFile(filepath.Join(appDir, ps1.TitleConfigFile), []byte(cfg)); err != nil {
			return fmt.Errorf("write %s: %w", ps1.TitleConfigFile, err)
		}
		manifest.Apps[a.ID] = homebrew.Record{Version: rel.Version, ELF: a.ELFName, Installed: time.Now().UTC()}
		return homebrew.WriteManifest(mp, manifest)
	})
	if err != nil {
		return rep, err
	}
	logging.ContextLogger(ctx).Info("installed homebrew app",
		"id", a.ID, "from", rep.From, "to", rel.Version)
	return rep, nil
}

// downloadTo fetches a URL into the scratch directory.
func (s *Services) downloadTo(ctx context.Context, url, name string) (string, func(), error) {
	root, err := s.ScratchRoot()
	if err != nil {
		return "", nil, err
	}
	// ScratchRoot names the directory; it does not create it, and a download
	// may be the first thing on a drive that has never staged anything.
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "download-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("download %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		cleanup()
		return "", nil, fmt.Errorf("download %s: %s", name, resp.Status)
	}
	path := filepath.Join(dir, name)
	f, err := os.Create(path)
	if err != nil {
		cleanup()
		return "", nil, err
	}
	_, err = io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("download %s: %w", name, err)
	}
	return path, cleanup, nil
}
