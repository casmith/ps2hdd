package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

func newSetupCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure ps2hdd",
		Long: `Set up the pieces ps2hdd needs.

With no subcommand this reports on the current configuration and what is still
missing.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep := buildDoctorReport(cmd.Context(), env)
			if env.JSON {
				return env.emitJSON(rep)
			}
			renderDoctor(env, rep)
			if len(rep.Problems) > 0 {
				env.printf("\n%s\n", dim("Run `ps2hdd detect --configure` to pick a drive, `ps2hdd config set` to"))
				env.printf("%s\n", dim("point at your source directories, and `ps2hdd setup ps1` for PS1 support."))
			}
			return nil
		},
	}
	cmd.AddCommand(newSetupPS1Command(env))
	return cmd
}

func newSetupPS1Command(env *Env) *cobra.Command {
	var importDir string
	var createPOPS string
	var launchers bool
	cmd := &cobra.Command{
		Use:   "ps1",
		Short: "Check PS1/POPStarter support and import runtime files",
		Long: `Report whether PlayStation 1 support is ready, and optionally import the POPS
runtime from a directory you supply:

  ps2hdd setup ps1
  ps2hdd setup ps1 --import ~/pops-files

POPS.ELF and IOPRP252.IMG are Sony code. ps2hdd does not, and will not, ship
them: you must obtain them yourself, from your own console or an official
source. POPSTARTER.ELF comes from the POPStarter release.

Only files whose names match the runtime are copied. Everything else in the
import directory is listed and left alone, so nothing unexpected ends up in a
partition the console boots from.

--create-pops creates the __.POPS partition the VCDs live in, at the size you
give it:

  ps2hdd setup ps1 --create-pops 20G

Size it for the library you intend to keep: a VCD is a raw 2352-bytes-per-sector
image, so budget around 750 MB per disc. APA allocates in 128 MiB units, so the
size must be a multiple of that; every whole number of gigabytes is. Growing the
partition afterwards needs a PS2-side tool, so err large.

--launchers writes the missing POPStarter launchers for PS1 games that are
already installed:

  ps2hdd setup ps1 --launchers

OPL has no PS1 support of its own, so a VCD in __.POPS appears in no menu. What
appears is a copy of POPSTARTER.ELF renamed after the VCD, in its own directory
under +OPL/APPS with a title.cfg beside it. Titles installed before ps2hdd
wrote those, or installed by another tool, need them filled in once. Titles
that already have a launcher are left alone.

The allocation itself is done by pfsshell, not by ps2hdd, for the same reason
installs go through hdl_dump: the reference implementation decides how APA
space is laid out. ps2hdd confirms the result by reading the partition table
back afterwards.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if createPOPS != "" {
				if err := createPOPSPartition(env, cmd, createPOPS); err != nil {
					return err
				}
			}
			if launchers {
				if err := repairLaunchers(env, cmd); err != nil {
					return err
				}
			}
			rep, err := env.Svc.SetupPS1(cmd.Context(), app.SetupPS1Options{
				ImportDir: config.ExpandPath(importDir),
			})
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(rep)
			}

			section(env.Out, "PS1 support")
			pairs := [][2]string{
				{ps1.POPSPartition, boolLabel(rep.Readiness.POPSPartition, "OK", "missing")},
				{ps1.CommonPartition, boolLabel(rep.Readiness.CommonPartition, "OK", "missing")},
			}
			wrong := map[string]bool{}
			for _, n := range rep.Readiness.Wrong {
				wrong[n] = true
			}
			for _, f := range ps1.RuntimeFiles {
				switch {
				case !rep.Readiness.RuntimeChecked:
					pairs = append(pairs, [2]string{f.Name, dim("unknown")})
				// A file that is present but is not the right file must not
				// read as OK: that is precisely the state this check exists to
				// expose, and it looks identical to a working one otherwise.
				case wrong[f.Name]:
					pairs = append(pairs, [2]string{f.Name, red("WRONG FILE")})
				case rep.Readiness.Runtime[f.Name]:
					pairs = append(pairs, [2]string{f.Name, green("OK")})
				default:
					pairs = append(pairs, [2]string{f.Name, amber("MISSING")})
				}
			}
			kv(env.Out, pairs)

			env.printf("\n%s %s\n", bold("Status:"), colorStatus(rep.Readiness.Status()))
			for _, line := range rep.Readiness.Explain() {
				env.printf("  %s %s\n", dim("-"), line)
			}

			if len(rep.Imported) > 0 {
				verb := "Imported"
				if rep.DryRun {
					verb = "Would import"
				}
				section(env.Out, verb)
				for _, f := range rep.Imported {
					env.printf("  %s\n", f)
				}
			}
			if len(rep.Ignored) > 0 {
				section(env.Out, "Ignored (not part of the POPS runtime)")
				for _, f := range rep.Ignored {
					env.printf("  %s\n", dim(f))
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&importDir, "import", "", "copy POPS runtime files from this directory onto the HDD")
	cmd.Flags().StringVar(&createPOPS, "create-pops", "",
		"create the __.POPS partition at this size (e.g. 20G) before checking readiness")
	cmd.Flags().BoolVar(&launchers, "launchers", false,
		"write POPStarter launchers for installed PS1 games that have none")
	return cmd
}

// repairLaunchers runs the launcher half of `setup ps1`.
func repairLaunchers(env *Env, cmd *cobra.Command) error {
	fixed, err := env.Svc.RepairPS1Launchers(cmd.Context())
	if err != nil {
		return err
	}
	verb := "Wrote a launcher for"
	if env.Svc.DryRun {
		verb = "Would write a launcher for"
	}
	if len(fixed) == 0 {
		env.printf("%s\n", dim("Every installed PS1 game already has a launcher."))
		return nil
	}
	section(env.Out, "Launchers")
	for _, t := range fixed {
		env.printf("  %s %s\n", dim(verb), t)
	}
	return nil
}

// effectiveScratch shows where extraction will actually write. An unset value
// is not unknown -- it has a default, and that default is what a reader needs
// when deciding whether there is room in it.
func effectiveScratch(env *Env) string {
	if env.Config.Install.ScratchDir != "" {
		return env.Config.Install.ScratchDir
	}
	root, err := env.Svc.ScratchRoot()
	if err != nil {
		return dim("unknown")
	}
	return root + dim(" (default)")
}

// createPOPSPartition runs the partition creation half of `setup ps1`.
//
// It is a write to the drive, so it confirms first unless the user has opted
// out, and it reports what happened before readiness is re-checked -- the
// readiness block printed afterwards is the proof it worked.
func createPOPSPartition(env *Env, cmd *cobra.Command, size string) error {
	normalised, err := app.NormalisePartitionSize(size)
	if err != nil {
		return err
	}
	if !env.Svc.DryRun && !env.Svc.AssumeYes && env.Config.TUI.ConfirmDestructiveActions {
		env.printf("Create the %s partition (%s) on %s?\n", ps1.POPSPartition, normalised, env.Config.Device)
		env.printf("%s\n", dim("Existing partitions are not touched, but this writes to the APA table."))
		if !confirm(env.In, env.Out, "Proceed?") {
			return fmt.Errorf("cancelled")
		}
	}

	rep, err := env.Svc.CreatePOPSPartition(cmd.Context(), size)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.emitJSON(rep)
	}
	if rep.DryRun {
		section(env.Out, "Would create")
		env.printf("  %s at %s, with:\n", rep.Partition, rep.Size)
		for _, line := range strings.Split(strings.TrimSpace(rep.Script), "\n") {
			env.printf("    %s\n", dim(line))
		}
		return nil
	}
	env.printf("Created %s (%s).\n", rep.Partition, rep.Size)
	return nil
}

func newConfigCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Read and write the configuration file",
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "show",
			Short: "Print the effective configuration",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, args []string) error {
				if env.JSON {
					return env.emitJSON(env.Config)
				}
				section(env.Out, "Configuration")
				kv(env.Out, [][2]string{
					{"File", env.Config.Path()},
					{"device", orDash(env.Config.Device)},
					{"sources.ps2", orDash(env.Config.Sources.PS2)},
					{"sources.ps1", orDash(env.Config.Sources.PS1)},
					{"install.sync_assets", fmt.Sprintf("%v", env.Config.Install.SyncAssets)},
					{"install.verify_after_install", fmt.Sprintf("%v", env.Config.Install.VerifyAfterInstall)},
					{"install.widescreen", fmt.Sprintf("%v", env.Config.Install.Widescreen)},
					{"install.prefetch", fmt.Sprintf("%d", env.Config.Install.Prefetch)},
					{"install.scratch_dir", effectiveScratch(env)},
					{"assets.provider", env.Config.Assets.Provider},
					{"assets.mirror", orDash(env.Config.Assets.Mirror)},
					{"assets.covers", fmt.Sprintf("%v", env.Config.Assets.Covers)},
					{"assets.backgrounds", fmt.Sprintf("%v", env.Config.Assets.Backgrounds)},
					{"assets.screenshots", fmt.Sprintf("%v", env.Config.Assets.Screenshots)},
					{"assets.icons", fmt.Sprintf("%v", env.Config.Assets.Icons)},
					{"assets.logos", fmt.Sprintf("%v", env.Config.Assets.Logos)},
					{"assets.config", fmt.Sprintf("%v", env.Config.Assets.Config)},
					{"tools.sudo", fmt.Sprintf("%v", env.Config.Tools.Sudo)},
					{"tui.confirm_destructive_actions", fmt.Sprintf("%v", env.Config.TUI.ConfirmDestructiveActions)},
				})
				return nil
			},
		},
		newConfigSetCommand(env),
	)
	return cmd
}

func newConfigSetCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: `Set one configuration key, for example:

  ps2hdd config set sources.ps2 /mnt/nas/games/ps2
  ps2hdd config set sources.ps1 /mnt/nas/games/psx
  ps2hdd config set assets.backgrounds true
  ps2hdd config set assets.mirror ~/opl-art

The device key is validated: only stable identifiers are accepted.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := env.Config
			if err := setConfigKey(&cfg, args[0], args[1]); err != nil {
				return err
			}
			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			env.printf("Set %s in %s.\n", args[0], cfg.Path())
			return nil
		},
	}
}

func setConfigKey(cfg *config.Config, key, value string) error {
	parseBool := func() (bool, error) {
		switch value {
		case "true", "yes", "on", "1":
			return true, nil
		case "false", "no", "off", "0":
			return false, nil
		}
		return false, fmt.Errorf("%s expects true or false, got %q", key, value)
	}
	switch key {
	case "device":
		if err := config.ValidateDevice(value); err != nil {
			return err
		}
		cfg.Device = value
	case "sources.ps2":
		cfg.Sources.PS2 = config.ExpandPath(value)
	case "sources.ps1":
		cfg.Sources.PS1 = config.ExpandPath(value)
	case "assets.provider":
		cfg.Assets.Provider = value
	case "assets.mirror":
		cfg.Assets.Mirror = config.ExpandPath(value)
	case "install.prefetch":
		n, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || n < 0 {
			return fmt.Errorf("install.prefetch takes a whole number of titles, not %q", value)
		}
		cfg.Install.Prefetch = n
	case "install.scratch_dir":
		cfg.Install.ScratchDir = config.ExpandPath(value)
	case "tools.hdl_dump":
		cfg.Tools.HDLDump = value
	case "tools.pfsfuse":
		cfg.Tools.PFSFuse = value
	case "tools.cue2pops":
		cfg.Tools.Cue2POPS = value
	default:
		b, err := parseBool()
		if err != nil {
			return fmt.Errorf("unknown configuration key %q", key)
		}
		switch key {
		case "install.sync_assets":
			cfg.Install.SyncAssets = b
		case "install.verify_after_install":
			cfg.Install.VerifyAfterInstall = b
		case "install.widescreen":
			cfg.Install.Widescreen = b
		case "assets.covers":
			cfg.Assets.Covers = b
		case "assets.backgrounds":
			cfg.Assets.Backgrounds = b
		case "assets.screenshots":
			cfg.Assets.Screenshots = b
		case "assets.back_covers":
			cfg.Assets.BackCovers = b
		case "assets.discs", "assets.icons":
			// `icons` is the former name; both are accepted so a documented
			// command from an older guide still does what it says.
			cfg.Assets.Discs = b
		case "assets.logos":
			cfg.Assets.Logos = b
		case "assets.spines":
			cfg.Assets.Spines = b
		case "assets.config":
			cfg.Assets.Config = b
		case "tools.sudo":
			cfg.Tools.Sudo = b
		case "tui.confirm_destructive_actions":
			cfg.TUI.ConfirmDestructiveActions = b
		default:
			return fmt.Errorf("unknown configuration key %q", key)
		}
	}
	return nil
}
