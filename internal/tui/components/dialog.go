package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// DialogKind changes a dialog's colour and default button.
type DialogKind int

const (
	// DialogConfirm asks a yes/no question and defaults to no.
	DialogConfirm DialogKind = iota
	// DialogDanger is a confirmation for something irreversible. It defaults
	// to no and is drawn in the danger colour.
	DialogDanger
	// DialogError reports a failure and has only a dismiss action.
	DialogError
	// DialogInfo reports something harmless.
	DialogInfo
)

// Dialog is a modal overlay.
type Dialog struct {
	Kind  DialogKind
	Title string
	// Body is the main text, already wrapped by the caller or short enough not
	// to need it.
	Body string
	// Details holds key/value rows, used to show exactly what is about to be
	// deleted before a destructive action is confirmed.
	Details [][2]string
	// Confirm and Cancel label the buttons.
	Confirm string
	Cancel  string
	// OnConfirm is the action key the view dispatches on; the dialog itself
	// performs nothing.
	OnConfirm string
	// Payload carries whatever the view needs to complete the action.
	Payload any

	// yes tracks which button is highlighted.
	yes bool
}

// NewConfirm builds a yes/no dialog defaulting to no.
func NewConfirm(title, body string, details [][2]string, action string, payload any) *Dialog {
	return &Dialog{
		Kind: DialogConfirm, Title: title, Body: body, Details: details,
		Confirm: "Yes", Cancel: "No", OnConfirm: action, Payload: payload,
	}
}

// NewDanger builds a confirmation for an irreversible action. It defaults to
// the cancel button: a stray Enter must never delete a game.
func NewDanger(title, body string, details [][2]string, action string, payload any) *Dialog {
	d := NewConfirm(title, body, details, action, payload)
	d.Kind = DialogDanger
	return d
}

// NewError builds a dismissable error dialog.
func NewError(title, body string) *Dialog {
	return &Dialog{Kind: DialogError, Title: title, Body: body, Cancel: "Dismiss"}
}

// NewInfo builds a dismissable informational dialog.
func NewInfo(title, body string) *Dialog {
	return &Dialog{Kind: DialogInfo, Title: title, Body: body, Cancel: "OK"}
}

// HasChoice reports whether the dialog offers a confirm action.
func (d *Dialog) HasChoice() bool { return d.OnConfirm != "" }

// Toggle moves between the buttons.
func (d *Dialog) Toggle() { d.yes = !d.yes }

// SetYes highlights a specific button.
func (d *Dialog) SetYes(v bool) { d.yes = v }

// Confirmed reports whether the confirm button is highlighted.
func (d *Dialog) Confirmed() bool { return d.yes }

// View renders the dialog centred in a width x height area.
func (d *Dialog) View(width, height int) string {
	accent := ColorAccent
	switch d.Kind {
	case DialogDanger, DialogError:
		accent = ColorDanger
	case DialogInfo:
		accent = ColorAccent
	}

	// The box has a border and 3 cells of padding on each side, so the text
	// area is 8 cells narrower than the box. Long values -- a device path, in
	// particular -- are truncated to it rather than allowed to push the box
	// past the edge of the terminal.
	const chrome = 8
	// Leave a small margin either side of the centred box, but not so much
	// that an 80-column terminal squeezes the text into half its width.
	inner := width - chrome - 8
	if inner > 78 {
		inner = 78
	}
	if inner < 24 {
		inner = 24
	}

	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(accent).Render(d.Title))
	if d.Body != "" {
		b.WriteString("\n\n")
		b.WriteString(lipgloss.NewStyle().Width(inner).Render(d.Body))
	}
	if len(d.Details) > 0 {
		b.WriteString("\n")
		keyWidth := 0
		for _, kv := range d.Details {
			if len(kv[0]) > keyWidth {
				keyWidth = len(kv[0])
			}
		}
		valueWidth := inner - keyWidth - 2
		if valueWidth < 8 {
			valueWidth = 8
		}
		for _, kv := range d.Details {
			b.WriteString("\n" + StyleMuted.Render(Pad(kv[0], keyWidth)) + "  " +
				Truncate(kv[1], valueWidth))
		}
	}

	b.WriteString("\n\n")
	if d.HasChoice() {
		b.WriteString(button(d.Cancel, !d.yes, ColorMuted))
		b.WriteString("  ")
		b.WriteString(button(d.Confirm, d.yes, accent))
		b.WriteString("\n\n")
		b.WriteString(StyleMuted.Render("←/→ or tab to choose · enter to accept · esc to cancel"))
	} else {
		b.WriteString(button(d.Cancel, true, accent))
		b.WriteString("\n\n")
		b.WriteString(StyleMuted.Render("enter or esc to dismiss"))
	}

	// The box must fit the terminal in both directions. Height is capped by
	// trimming the body rather than letting the box run off the top, which is
	// how a long help text would otherwise become unreadable.
	body := b.String()
	if maxLines := height - 6; maxLines > 4 {
		if lines := strings.Split(body, "\n"); len(lines) > maxLines {
			trimmed := append(lines[:maxLines-1:maxLines-1],
				StyleMuted.Render("… (window too short to show it all)"))
			body = strings.Join(trimmed, "\n")
		}
	}

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accent).
		Padding(1, 3).
		MaxWidth(width).
		MaxHeight(height).
		Render(body)

	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

func button(label string, active bool, accent lipgloss.TerminalColor) string {
	s := lipgloss.NewStyle().Padding(0, 2)
	if active {
		return s.Bold(true).Foreground(lipgloss.Color("232")).Background(accent).Render(label)
	}
	return s.Foreground(ColorMuted).Render(label)
}
