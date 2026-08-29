package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

func newStatusCommand(env *Env) *cobra.Command {
	var showPartitions bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show the configured HDD's status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := env.Svc.Status(cmd.Context())
			if err != nil {
				return err
			}
			ready, readyErr := env.Svc.PS1Readiness(cmd.Context())

			if env.JSON {
				return env.emitJSON(struct {
					model.DriveStatus
					PS1 ps1.Readiness `json:"ps1"`
				}{st, ready})
			}

			section(env.Out, "Drive")
			kv(env.Out, [][2]string{
				{"Device", st.ByID},
				{"Resolved", st.DevicePath},
				{"Model", orDash(st.Model)},
				{"Serial", orDash(st.Serial)},
				{"Capacity", model.HumanSize(st.SizeBytes)},
			})

			section(env.Out, "Layout")
			kv(env.Out, [][2]string{
				{"APA", boolLabel(st.APADetected, "detected", "not detected")},
				{drive.PartitionOPL, boolLabel(st.HasOPL, "detected", "missing")},
				{ps1.POPSPartition, boolLabel(st.HasPOPS, "detected", "missing")},
				{ps1.CommonPartition, boolLabel(st.HasCommon, "detected", "missing")},
				{"Partitions", fmt.Sprintf("%d", len(st.Partitions))},
			})

			section(env.Out, "Storage")
			kv(env.Out, [][2]string{
				{"Allocatable", model.HumanSize(st.TotalBytes)},
				{"Used", model.HumanSize(st.UsedBytes)},
				{"Free", model.HumanSize(st.FreeBytes)},
			})

			section(env.Out, "Library")
			kv(env.Out, [][2]string{
				{"PS2 games", fmt.Sprintf("%d", st.PS2Games)},
				{"PS1 games", fmt.Sprintf("%d", st.PS1Games)},
			})

			section(env.Out, "Support")
			ps1Status := ready.Status()
			if readyErr != nil {
				ps1Status = "unknown: " + readyErr.Error()
			}
			kv(env.Out, [][2]string{
				{"PS2", boolLabel(st.APADetected, "READY", "NOT READY")},
				{"PS1", colorStatus(ps1Status)},
			})
			if readyErr == nil {
				for _, line := range ready.Explain() {
					env.printf("  %s %s\n", dim("-"), line)
				}
			}

			if showPartitions {
				section(env.Out, "Partitions")
				t := newTable(env.Out)
				fmt.Fprintln(t, bold("ID\tTYPE\tSIZE\tSTART\tSLICE"))
				for _, p := range st.Partitions {
					fmt.Fprintf(t, "%s\t%s\t%s\t%d\t%d\n",
						p.ID, partitionType(p.Type), model.HumanSize(p.TotalBytes), p.StartSector, p.Slice)
				}
				t.Flush()
			}

			for _, n := range st.Notes {
				env.printf("\n%s %s\n", amber("note:"), n)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&showPartitions, "partitions", false, "list the APA partition table")
	return cmd
}

func boolLabel(ok bool, yes, no string) string {
	if ok {
		return green(yes)
	}
	return amber(no)
}

func colorStatus(s string) string {
	if s == "READY" {
		return green(s)
	}
	return amber(s)
}

// partitionType names the APA partition types ps2hdd cares about.
func partitionType(t uint16) string {
	switch t {
	case 0x0001:
		return "MBR"
	case 0x0100:
		return "PFS"
	case 0x1337:
		return "HDL game"
	case 0x0082:
		return "swap"
	case 0x0083:
		return "linux"
	default:
		return fmt.Sprintf("0x%04x", t)
	}
}
