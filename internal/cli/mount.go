package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/drive"
)

func newMountCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount [partition]",
		Short: "Mount a PFS partition and print its mountpoint",
		Long: `Mount a PFS partition from the configured HDD with pfsfuse and print where
it landed, so a shell can use it:

  cd "$(ps2hdd mount +OPL)"
  ps2hdd mount __.POPS

The mount stays after ps2hdd exits; release it with ` + "`ps2hdd unmount`" + `.
It lives under $XDG_RUNTIME_DIR/ps2hdd/mnt/, and unmount only ever touches
paths under there -- a mount you made yourself is never disturbed.

Mounts ps2hdd makes for its own work (installing, syncing artwork) are separate
and are always released when the command finishes.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			partition := drive.PartitionOPL
			if len(args) == 1 {
				partition = args[0]
			}
			m, err := env.Svc.Mounts(cmd.Context())
			if err != nil {
				return err
			}
			mp, err := m.MountPersistent(cmd.Context(), partition)
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(map[string]string{"partition": partition, "mountpoint": mp})
			}
			// The path alone on stdout, so it can be captured in a shell.
			env.printf("%s\n", mp)
			env.warnf("%s release it with `ps2hdd unmount %s`\n", dim("note:"), partition)
			return nil
		},
	}
	return cmd
}

func newUnmountCommand(env *Env) *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:     "unmount [partition]",
		Aliases: []string{"umount"},
		Short:   "Release a PFS mount made by `ps2hdd mount`",
		Long: `Release a mount created by ` + "`ps2hdd mount`" + `.

Only mountpoints under $XDG_RUNTIME_DIR/ps2hdd/mnt/ are touched. A mount you
created yourself with pfsfuse is left alone; use fusermount3 -u for those.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := env.Svc.Mounts(cmd.Context())
			if err != nil {
				return err
			}

			if len(args) == 1 {
				target := args[0]
				// An absolute path is treated as a path so the containment
				// refusal is explicit, rather than being silently reinterpreted
				// as a partition name.
				if strings.HasPrefix(target, "/") {
					if err := m.UnmountPath(cmd.Context(), target); err != nil {
						return err
					}
				} else if err := m.UnmountPersistent(cmd.Context(), target); err != nil {
					return err
				}
				if env.JSON {
					return env.emitJSON(map[string]any{"unmounted": []string{target}})
				}
				env.printf("Released %s.\n", target)
				return nil
			}

			mounts, err := m.ListPersistent()
			if err != nil {
				return err
			}
			if len(mounts) == 0 {
				if env.JSON {
					return env.emitJSON(map[string]any{"unmounted": []string{}})
				}
				env.printf("Nothing to release.\n")
				return nil
			}
			if !all && len(mounts) > 1 {
				names := make([]string, 0, len(mounts))
				for n := range mounts {
					names = append(names, n)
				}
				sort.Strings(names)
				return fmt.Errorf("%d mounts are held: %s\n\nName one, or pass --all",
					len(mounts), strings.Join(names, ", "))
			}

			names := make([]string, 0, len(mounts))
			for n := range mounts {
				names = append(names, n)
			}
			sort.Strings(names)

			var released []string
			for _, n := range names {
				// The stable directory name maps back to a partition through
				// the same transformation that produced it, so unmounting is
				// done by path rather than by guessing the partition id.
				if err := m.UnmountPath(cmd.Context(), mounts[n]); err != nil {
					return err
				}
				released = append(released, n)
			}
			if env.JSON {
				return env.emitJSON(map[string]any{"unmounted": released})
			}
			for _, n := range released {
				env.printf("Released %s.\n", n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&all, "all", "a", false, "release every mount ps2hdd is holding")
	return cmd
}
