package cli

import (
	"fmt"

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
partition the console boots from.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
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
			for _, f := range ps1.RuntimeFiles {
				switch {
				case !rep.Readiness.RuntimeChecked:
					pairs = append(pairs, [2]string{f.Name, dim("unknown")})
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
	return cmd
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
		case "assets.covers":
			cfg.Assets.Covers = b
		case "assets.backgrounds":
			cfg.Assets.Backgrounds = b
		case "assets.screenshots":
			cfg.Assets.Screenshots = b
		case "assets.icons":
			cfg.Assets.Icons = b
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
