package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

func (m *Model) handleDriveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyMatches(msg, "p"):
		m.showPartitions = !m.showPartitions
	case keyMatches(msg, "s"):
		return m, m.checkPS1Setup()
	}
	return m, nil
}

func (m *Model) renderDrive() string {
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render("Drive"))
	if m.loadingDrive {
		b.WriteString("  " + components.Spinner(m.tickCount))
	}
	b.WriteString("\n\n")

	if m.driveErr != nil {
		// A safety refusal is already a complete, multi-line explanation; it
		// is shown verbatim rather than squeezed into one line.
		b.WriteString(components.StyleDanger.Render(m.driveErr.Error()))
		return b.String()
	}
	st := m.driveStatus
	if st.DevicePath == "" {
		b.WriteString(components.StyleWarning.Render("No PS2 HDD is configured."))
		b.WriteString("\n\n" + components.StyleMuted.Render(
			"Run `ps2hdd detect --configure` in a shell to pick one.\n"+
				"ps2hdd only ever stores a stable /dev/disk/by-id path."))
		return b.String()
	}

	w := m.contentWidth()
	b.WriteString(renderKV([][2]string{
		{"Device", st.ByID},
		{"Resolved", st.DevicePath},
		{"Model", orUnknown(st.Model)},
		{"Serial", orUnknown(st.Serial)},
		{"Capacity", model.HumanSize(st.SizeBytes)},
	}, w))

	b.WriteString("\n\n" + components.StyleHeader.Render("Layout") + "\n")
	b.WriteString(renderKV([][2]string{
		{"APA", okLabel(st.APADetected, "detected", "not detected")},
		{drive.PartitionOPL, okLabel(st.HasOPL, "detected", "missing")},
		{ps1.POPSPartition, okLabel(st.HasPOPS, "detected", "missing")},
		{ps1.CommonPartition, okLabel(st.HasCommon, "detected", "missing")},
	}, w))

	b.WriteString("\n\n" + components.StyleHeader.Render("Storage") + "\n")
	usedFrac := 0.0
	if st.TotalBytes > 0 {
		usedFrac = float64(st.UsedBytes) / float64(st.TotalBytes)
	}
	barWidth := m.contentWidth() - 30
	if barWidth < 10 {
		barWidth = 10
	}
	b.WriteString("\n  " + components.Bar(barWidth, usedFrac) + "  " +
		components.StyleMuted.Render(fmt.Sprintf("%s of %s used",
			model.HumanSize(st.UsedBytes), model.HumanSize(st.TotalBytes))))
	b.WriteString("\n\n" + renderKV([][2]string{
		{"Free", model.HumanSize(st.FreeBytes)},
		{"PS2 games", fmt.Sprintf("%d", st.PS2Games)},
		{"PS1 games", fmt.Sprintf("%d", st.PS1Games)},
	}, w))

	b.WriteString("\n\n" + components.StyleHeader.Render("Support") + "\n")
	b.WriteString(renderKV([][2]string{
		{"PS2", okLabel(st.APADetected, "READY", "NOT READY")},
		{"PS1", okLabel(m.ps1Ready.Ready(), "READY", "NOT READY")},
	}, w))
	for _, line := range m.ps1Ready.Explain() {
		b.WriteString("\n  " + components.StyleMuted.Render("- "+
			components.Truncate(line, m.contentWidth()-4)))
	}

	if m.showPartitions && len(st.Partitions) > 0 {
		b.WriteString("\n\n" + components.StyleHeader.Render("Partitions") + "\n")
		for _, p := range st.Partitions {
			b.WriteString(fmt.Sprintf("\n  %-22s %-10s %10s",
				components.Truncate(p.ID, 22), partitionTypeName(p.Type), model.HumanSize(p.TotalBytes)))
		}
	}

	for _, n := range st.Notes {
		b.WriteString("\n\n" + components.StyleWarning.Render("note: ") +
			components.StyleMuted.Render(n))
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return components.StyleMuted.Render("unknown")
	}
	return s
}

func okLabel(ok bool, yes, no string) string {
	if ok {
		return components.StyleSuccess.Render(yes)
	}
	return components.StyleWarning.Render(no)
}

func partitionTypeName(t uint16) string {
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
