package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/model"
)

func newDetectCommand(env *Env) *cobra.Command {
	var configure bool
	var all bool

	cmd := &cobra.Command{
		Use:   "detect",
		Short: "Find candidate PS2 HDDs (read-only)",
		Long: `Enumerate the machine's disks and report which ones carry an APA partition
table, without writing anything.

Disks that back mounted Linux filesystems are skipped rather than probed: a
disk carrying the running system is not a PS2 HDD, and ps2hdd will not touch
one under any circumstances.

With --configure, the single candidate found is written to the config file as a
stable /dev/disk/by-id path. Kernel names such as /dev/sdb are never persisted:
they are reassigned between boots.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			candidates, err := env.Svc.Detect(cmd.Context())
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(candidates)
			}

			var usable []drive.Candidate
			for _, c := range candidates {
				if c.IsCandidate() {
					usable = append(usable, c)
				}
			}

			if len(usable) == 0 {
				env.printf("No PS2 HDD found.\n")
				if len(candidates) > 0 {
					env.printf("\nDisks examined:\n")
					renderCandidates(env, candidates, true)
				}
				env.printf("\nIf your PS2 HDD is connected, check that:\n")
				env.printf("  - it is attached and powered\n")
				env.printf("  - you can read the raw device (try sudo, or see docs/safety.md)\n")
				env.printf("  - it really is APA formatted; ps2hdd will not format it for you\n")
				return nil
			}

			renderCandidates(env, candidates, all)

			if !configure {
				if len(usable) == 1 {
					env.printf("\nTo use this drive:\n  ps2hdd detect --configure\n")
				}
				return nil
			}
			if len(usable) > 1 {
				return fmt.Errorf("found %d candidate PS2 HDDs; set `device` in %s by hand rather than have ps2hdd guess",
					len(usable), env.Config.Path())
			}
			c := usable[0]
			if c.ByID == "" {
				return fmt.Errorf("%s has no /dev/disk/by-id entry, so there is no stable way to name it; "+
					"ps2hdd will not persist a kernel device name", c.Device.Path)
			}
			cfg := env.Config
			cfg.Device = c.ByID
			if err := cfg.Save(); err != nil {
				return err
			}
			env.printf("\n%s\n  %s\n", bold("Configured device:"), c.ByID)
			env.printf("  written to %s\n", cfg.Path())
			return nil
		},
	}
	cmd.Flags().BoolVar(&configure, "configure", false, "save the detected drive to the config file")
	cmd.Flags().BoolVar(&all, "all", false, "list every disk, including skipped ones")
	return cmd
}

func renderCandidates(env *Env, candidates []drive.Candidate, includeSkipped bool) {
	for _, c := range candidates {
		if !c.IsCandidate() && !includeSkipped {
			continue
		}
		title := "Candidate PS2 HDD"
		if !c.IsCandidate() {
			title = "Not a candidate"
		}
		section(env.Out, title)
		pairs := [][2]string{
			{"Device", c.Device.Path},
			{"Model", orDash(c.Device.Model)},
			{"Serial", orDash(c.Device.Serial)},
			{"Capacity", model.HumanSize(c.Device.SizeBytes)},
		}
		if c.ByID != "" {
			pairs = append(pairs, [2]string{"Stable path", c.ByID})
		} else {
			pairs = append(pairs, [2]string{"Stable path", dim("none (udev created no by-id link)")})
		}
		switch {
		case c.Skipped != "":
			pairs = append(pairs, [2]string{"Skipped", amber(c.Skipped)})
		case c.ReadError != "":
			pairs = append(pairs, [2]string{"APA", amber("could not read: " + c.ReadError)})
		case c.APA:
			pairs = append(pairs, [2]string{"APA", green("detected")})
		default:
			pairs = append(pairs, [2]string{"APA", "not detected"})
		}
		kv(env.Out, pairs)
	}
}

func orDash(s string) string {
	if s == "" {
		return dim("unknown")
	}
	return s
}
