package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/homebrew"
)

func newAppsCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "apps",
		Short: "Install and update the homebrew that runs alongside the games",
		Long: `Homebrew programs live in +OPL/APPS, one directory each, and OPL lists them
on its Apps page. That is the same mechanism PS1 titles get their launchers
through.

ps2hdd records the version it installed, because these ELFs carry no version
string that can be read back: OPL is a rolling build whose only version appears
in the filename of an archive published beside it.

Installing here is not the same as changing what the console boots. A newer OPL
installed as an app can be launched from the current one and tried before
anything about the boot path changes, which is the order worth doing it in.`,
	}
	cmd.AddCommand(newAppsListCommand(env), newAppsUpdateCommand(env))
	return cmd
}

func newAppsListCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "Show what is installed and what upstream offers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := env.Svc.AppStatus(cmd.Context())
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(rows)
			}
			w := newTable(env.Out)
			fmt.Fprintln(w, "APP\tINSTALLED\tLATEST\t")
			for _, r := range rows {
				fmt.Fprintf(w, "%s\t%s\t%s\t\n", r.App.Name, installedLabel(r), latestLabel(r))
			}
			if err := w.Flush(); err != nil {
				return err
			}
			for _, r := range rows {
				if r.Err != nil {
					env.warnf("  %s could not be checked: %v\n", amber(r.App.Name), r.Err)
				}
			}
			return nil
		},
	}
}

// installedLabel says what is on the drive. A recorded version whose file is
// gone is a broken install, not an installed app, and must not read as one.
func installedLabel(r homebrew.Status) string {
	switch {
	case !r.Present && r.Installed != "":
		return red("missing (recorded " + r.Installed + ")")
	case !r.Present:
		return dim("not installed")
	case r.Installed == "":
		return amber("unknown version")
	}
	return r.Installed
}

func latestLabel(r homebrew.Status) string {
	switch {
	case r.Err != nil:
		return dim("unknown")
	case r.UpdateAvailable():
		return amber(r.Latest.Version + "  update available")
	case r.Present && r.Installed == r.Latest.Version:
		return green("up to date")
	}
	return r.Latest.Version
}

func newAppsUpdateCommand(env *Env) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "update <app>",
		Short: "Install the current build of an app into +OPL/APPS",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rep, err := env.Svc.UpdateApp(cmd.Context(), args[0], force)
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(rep)
			}
			switch {
			case rep.Skipped:
				env.printf("%s is already at %s.\n", rep.App.Name, rep.To)
				return nil
			case rep.DryRun:
				env.printf("Would install %s %s:\n", rep.App.Name, rep.To)
			case rep.From == "":
				env.printf("Installed %s %s:\n", rep.App.Name, rep.To)
			default:
				env.printf("Updated %s from %s to %s:\n", rep.App.Name, rep.From, rep.To)
			}
			for _, f := range rep.Files {
				env.printf("  %s\n", f)
			}
			if !rep.DryRun {
				env.printf("\n%s\n", dim("It is on OPL's Apps page. Launching it there leaves the boot path alone."))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even when the recorded version already matches")
	return cmd
}
