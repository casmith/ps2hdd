package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/cli"
	"github.com/casmith/ps2hdd/internal/logging"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

// run executes the CLI with the given arguments against a throwaway XDG
// environment, returning stdout, stderr and the error.
func run(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	return runIn(t, args...)
}

// runIn is run without a fresh environment, for tests that need several
// commands to share one demo HDD.
func runIn(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root, env := cli.NewRootCommand(nil)
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--no-color"}, args...))
	// The command writes through Env, whose streams are wired in
	// PersistentPreRunE; redirect them by hooking the same buffers.
	cli.SetTestStreams(&out, &errOut, strings.NewReader(""))
	defer cli.SetTestStreams(nil, nil, nil)
	err := root.ExecuteContext(context.Background())
	// Mounts must be released whether or not the command succeeded.
	_ = cli.Teardown(env)
	return out.String(), errOut.String(), err
}

func TestHelpListsEveryV1Command(t *testing.T) {
	out, _, err := run(t, "--help")
	if err != nil {
		t.Fatalf("--help: %v", err)
	}
	for _, cmd := range []string{
		"doctor", "detect", "status", "source", "list", "info",
		"install", "remove", "mount", "unmount", "art", "assets",
		"database", "setup", "config",
	} {
		if !strings.Contains(out, cmd) {
			t.Errorf("--help does not list %q", cmd)
		}
	}
}

// Read commands must offer --json so they can be scripted.
func TestJSONOutputs(t *testing.T) {
	for _, args := range [][]string{
		{"--demo", "--json", "status"},
		{"--demo", "--json", "list"},
		{"--demo", "--json", "source", "list"},
		{"--demo", "--json", "art", "status"},
		{"--demo", "--json", "assets", "status"},
		{"--demo", "--json", "info", "SLUS_210.50"},
		{"--demo", "--json", "setup", "ps1"},
		{"--json", "detect"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			out, _, err := run(t, args...)
			if err != nil {
				t.Fatalf("err = %v\nstdout: %s", err, out)
			}
			var v any
			if uerr := json.Unmarshal([]byte(out), &v); uerr != nil {
				t.Fatalf("output is not JSON: %v\n%s", uerr, out)
			}
		})
	}
}

// doctor exits non-zero when it finds problems, but its JSON is still valid so
// a script can read the findings.
func TestDoctorJSONWithProblems(t *testing.T) {
	out, _, err := run(t, "--demo", "--json", "doctor")
	if err == nil {
		t.Log("doctor reported no problems")
	}
	var rep cli.DoctorReport
	if uerr := json.Unmarshal([]byte(out), &rep); uerr != nil {
		t.Fatalf("doctor output is not JSON: %v\n%s", uerr, out)
	}
	if rep.Go == "" || rep.Config == "" {
		t.Errorf("doctor report is incomplete: %+v", rep)
	}
	if len(rep.Tools) == 0 {
		t.Error("doctor reported no tools")
	}
}

// A kernel device name must be refused with the standard refusal, not
// quietly accepted.
func TestRefusesUnstableDevice(t *testing.T) {
	_, _, err := run(t, "--device", "/dev/sdb", "status")
	if err == nil {
		t.Fatal("status accepted /dev/sdb")
	}
	if !strings.Contains(err.Error(), "REFUSING OPERATION") {
		t.Errorf("error was not a refusal:\n%v", err)
	}
}

func TestListFlagsAreExclusive(t *testing.T) {
	if _, _, err := run(t, "--demo", "list", "--ps1", "--ps2"); err == nil {
		t.Error("--ps1 and --ps2 were accepted together")
	}
	if _, _, err := run(t, "--demo", "list", "--installed", "--available"); err == nil {
		t.Error("--installed and --available were accepted together")
	}
}

func TestListFiltersByPlatform(t *testing.T) {
	out, _, err := run(t, "--demo", "list", "--ps1", "--no-artwork")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "PS2 ") {
		t.Errorf("--ps1 returned PS2 titles:\n%s", out)
	}
	if !strings.Contains(out, "PS1") {
		t.Errorf("--ps1 returned nothing:\n%s", out)
	}
}

// A dry run must print the exact command it would run and change nothing.
func TestInstallDryRun(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	demoRoot := filepath.Join(root, "cache", "ps2hdd", "demo")
	iso := filepath.Join(demoRoot, "sources", "ps2", "Shadow of the Colossus.iso")

	// The first invocation builds the demo environment.
	if _, _, err := runIn(t, "--demo", "status"); err != nil {
		t.Fatal(err)
	}
	out, _, err := runIn(t, "--demo", "--dry-run", "install", iso)
	if err != nil {
		t.Fatalf("dry-run install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Would install") {
		t.Errorf("dry run did not say what it would do:\n%s", out)
	}
	if !strings.Contains(out, "hdl_dump inject_dvd") {
		t.Errorf("dry run did not show the command:\n%s", out)
	}

	after, _, err := runIn(t, "--demo", "list", "--installed", "--no-artwork")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(after, "Shadow of the Colossus") {
		t.Errorf("the dry run installed the game:\n%s", after)
	}
}

// Removing by an ambiguous name must list the candidates and refuse.
func TestRemoveRefusesAmbiguity(t *testing.T) {
	_, _, err := run(t, "--demo", "--yes", "remove", "R")
	if err == nil {
		t.Fatal("an ambiguous name was accepted")
	}
	if !strings.Contains(err.Error(), "matches") {
		t.Errorf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "game ID") {
		t.Errorf("error is not actionable: %v", err)
	}
}

func TestConfigSetValidatesDevice(t *testing.T) {
	if _, _, err := run(t, "config", "set", "device", "/dev/sdb"); err == nil {
		t.Error("config set accepted a kernel device name")
	}
	if _, _, err := run(t, "config", "set", "nonsense.key", "x"); err == nil {
		t.Error("config set accepted an unknown key")
	}
	if _, _, err := run(t, "config", "set", "assets.covers", "maybe"); err == nil {
		t.Error("config set accepted a non-boolean for a boolean key")
	}
}

func TestConfigSetAndShow(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	src := filepath.Join(root, "games")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runIn(t, "config", "set", "sources.ps2", src); err != nil {
		t.Fatalf("config set: %v", err)
	}
	if _, _, err := runIn(t, "config", "set", "assets.backgrounds", "true"); err != nil {
		t.Fatalf("config set bool: %v", err)
	}
	out, _, err := runIn(t, "config", "show")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, src) {
		t.Errorf("config show does not report the source directory:\n%s", out)
	}
	if !strings.Contains(out, "assets.backgrounds") || !strings.Contains(out, "true") {
		t.Errorf("config show does not report the change:\n%s", out)
	}
}

func TestInfoOnAPath(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	if _, _, err := runIn(t, "--demo", "status"); err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(root, "cache", "ps2hdd", "demo", "sources", "ps2", "Gran Turismo 4.iso")
	// `info` on a path works without any HDD at all.
	out, _, err := runIn(t, "info", iso)
	if err != nil {
		t.Fatalf("info on a path: %v\n%s", err, out)
	}
	if !strings.Contains(out, "SCUS_973.28") {
		t.Errorf("info did not identify the image:\n%s", out)
	}
}

// The summary under a filtered listing has to describe what was shown. It used
// to count the whole catalog, so `list --installed` on a large library ended
// with "34 shown; 34 installed, 1926 available" -- naming the hundreds of
// titles the filter had just excluded, which reads as the filter doing nothing.
func TestListSummaryCountsOnlyWhatIsShown(t *testing.T) {
	all, _, err := run(t, "--demo", "list", "--no-artwork")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(all, "installed") || !strings.Contains(all, "available") {
		t.Fatalf("unfiltered summary should mention both:\n%s", all)
	}

	onlyInstalled, _, err := run(t, "--demo", "list", "--installed", "--no-artwork")
	if err != nil {
		t.Fatal(err)
	}
	summary := summaryLine(t, onlyInstalled)
	if strings.Contains(summary, "available") {
		t.Errorf("`list --installed` counts titles it excluded: %q", summary)
	}
	if !strings.Contains(summary, "installed") {
		t.Errorf("summary = %q, want an installed count", summary)
	}

	onlyAvailable, _, err := run(t, "--demo", "list", "--available", "--no-artwork")
	if err != nil {
		t.Fatal(err)
	}
	if s := summaryLine(t, onlyAvailable); strings.Contains(s, "installed") {
		t.Errorf("`list --available` counts installed titles: %q", s)
	}
}

func summaryLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "shown") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no summary line in:\n%s", out)
	return ""
}
