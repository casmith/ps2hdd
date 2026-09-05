// Package cli implements the ps2hdd command line.
//
// Every command here is a thin shell over internal/app: parse flags, call a
// service, render the result. No disk logic lives in this package, which is
// what keeps the CLI and the TUI honest about being two views of one program.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/demo"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/titles"
)

// Version is set at build time with -ldflags.
var Version = "dev"

// Env carries the process-wide state a command needs.
type Env struct {
	Config  config.Config
	Svc     *app.Services
	Out     io.Writer
	ErrOut  io.Writer
	In      io.Reader
	JSON    bool
	Verbose bool
	Debug   bool
	Quiet   bool
	// demoEnv is non-nil when --demo built a synthetic environment.
	demoEnv *demo.Env
}

// Services exposes the service layer to the TUI. It, with Config below,
// satisfies tui.EnvAdapter without either package importing the other's types.
func (e *Env) Services() *app.Services { return e.Svc }

// Config exposes the effective configuration to the TUI.
func (e *Env) ConfigValue() config.Config { return e.Config }

// globalFlags holds the values bound to the persistent flags.
type globalFlags struct {
	configPath string
	device     string
	dryRun     bool
	json       bool
	verbose    bool
	debug      bool
	yes        bool
	demo       bool
	noColor    bool
}

// NewRootCommand builds the command tree.
//
// Running ps2hdd with no arguments launches the TUI; every subcommand is the
// scriptable equivalent of something the TUI can do.
func NewRootCommand(runTUI func(*Env) error) (*cobra.Command, *Env) {
	var g globalFlags
	env := &Env{Out: os.Stdout, ErrOut: os.Stderr, In: os.Stdin}

	root := &cobra.Command{
		Use:   "ps2hdd",
		Short: "Manage a PlayStation 1/2 HDD from Linux",
		Long: `ps2hdd manages the games, artwork and metadata on a PlayStation 2 hard
drive: an internal SATA disk with an APA partition table, FreeHDBoot, Open PS2
Loader and POPStarter.

Run ps2hdd with no arguments to open the terminal interface. Every operation is
also available as a subcommand so it can be scripted.

Raw disk safety
  ps2hdd refuses to touch a disk it cannot positively identify as your PS2 HDD.
  It never formats, initialises or repairs an unrecognised disk, and it will not
  operate on a disk carrying mounted Linux filesystems. See docs/safety.md.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return setupEnv(env, &g)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if runTUI == nil {
				return cmd.Help()
			}
			return runTUI(env)
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&g.configPath, "config", "", "path to config.toml (default ~/.config/ps2hdd/config.toml)")
	pf.StringVar(&g.device, "device", "", "override the configured HDD (a /dev/disk/by-id/... path)")
	pf.BoolVar(&g.dryRun, "dry-run", false, "show what would happen without writing to the HDD")
	pf.BoolVar(&g.json, "json", false, "emit machine-readable JSON")
	pf.BoolVarP(&g.verbose, "verbose", "v", false, "log more detail")
	pf.BoolVar(&g.debug, "debug", false, "log to stderr as well as the log file")
	pf.BoolVarP(&g.yes, "yes", "y", false, "do not prompt for confirmation")
	pf.BoolVar(&g.demo, "demo", false, "run against a synthetic HDD and source library instead of real hardware")
	pf.BoolVar(&g.noColor, "no-color", false, "disable coloured output")

	root.AddCommand(
		newDoctorCommand(env),
		newDetectCommand(env),
		newStatusCommand(env),
		newSourceCommand(env),
		newListCommand(env),
		newInfoCommand(env),
		newInstallCommand(env),
		newRemoveCommand(env),
		newMountCommand(env),
		newUnmountCommand(env),
		newArtCommand(env),
		newAssetsCommand(env),
		newDatabaseCommand(env),
		newSetupCommand(env),
		newConfigCommand(env),
	)
	return root, env
}

func setupEnv(env *Env, g *globalFlags) error {
	env.applyTestStreams()
	if _, err := logging.Setup(logging.Options{Verbose: g.verbose, Debug: g.debug, Stderr: env.ErrOut}); err != nil {
		fmt.Fprintf(env.ErrOut, "warning: could not open the log file: %v\n", err)
	}

	cfg, err := config.Load(g.configPath)
	if err != nil {
		return err
	}
	if g.device != "" {
		cfg.Device = g.device
	}

	env.JSON, env.Verbose, env.Debug = g.json, g.verbose, g.debug
	if g.noColor {
		disableColor()
	}

	if g.demo {
		e, err := demo.Setup("")
		if err != nil {
			return fmt.Errorf("build the demo environment: %w", err)
		}
		env.demoEnv = e
		cfg = e.Config(cfg)
		env.Config = cfg
		env.Svc = app.New(cfg, e.Runner())
		// The demo is a closed world: it fakes the disk and the external
		// tools, and it must not reach a real title database either. Doing so
		// would make its output depend on the network and on what someone
		// else's repository says today.
		env.Svc.Titles = titles.NewOffline()
	} else {
		env.Config = cfg
		env.Svc = app.NewFromConfig(cfg)
	}
	env.Svc.DryRun = g.dryRun
	env.Svc.AssumeYes = g.yes
	return nil
}

// Teardown releases every PFS mount this process created and closes the log.
//
// It deliberately does not live in a Cobra PersistentPostRun hook: those do not
// run when a command returns an error, and a failed install is precisely when a
// leaked FUSE mount is most likely and most annoying. Execute defers this
// instead, so it runs on every path including a signal.
func Teardown(env *Env) error {
	if env.Svc != nil {
		// A cancelled context must not stop the unmount, so cleanup runs on a
		// fresh one.
		if err := env.Svc.Close(context.Background()); err != nil {
			fmt.Fprintf(env.ErrOut, "warning: could not release all mounts: %v\n", err)
		}
	}
	return logging.Close()
}

// Signals returns a context cancelled on SIGINT or SIGTERM, so that a running
// operation stops and, critically, so that PFS mounts are released rather than
// left behind holding the HDD busy.
func Signals(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// emitJSON writes a value as indented JSON.
func (e *Env) emitJSON(v any) error {
	enc := json.NewEncoder(e.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// printf writes to the command's output stream.
func (e *Env) printf(format string, args ...any) {
	fmt.Fprintf(e.Out, format, args...)
}

// warnf writes to the command's error stream.
func (e *Env) warnf(format string, args ...any) {
	fmt.Fprintf(e.ErrOut, format, args...)
}

// Execute runs the root command and turns errors into exit codes.
//
// A safety refusal is printed on its own, without the "Error:" prefix: it is
// not a malfunction, it is the program doing its job, and it already reads as
// a complete message.
func Execute(root *cobra.Command, env *Env) int {
	ctx, cancel := Signals(context.Background())
	defer cancel()
	defer func() { _ = Teardown(env) }()

	if err := root.ExecuteContext(ctx); err != nil {
		if drive.IsRefusal(err) {
			fmt.Fprintln(os.Stderr, err)
			return 3
		}
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}
	return 0
}
