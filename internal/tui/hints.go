package tui

import "github.com/casmith/ps2hdd/internal/tui/components"

// hints returns the footer key list for the active view. The list is
// view-specific because a footer that shows every binding all the time shows
// none of them usefully.
func (m *Model) hints() []components.Hint {
	if m.dialog != nil {
		if m.dialog.HasChoice() {
			return []components.Hint{
				{Key: "←→", Label: "choose"},
				{Key: "enter", Label: "accept"},
				{Key: "esc", Label: "cancel"},
			}
		}
		return []components.Hint{{Key: "enter", Label: "dismiss"}}
	}
	if m.searching {
		return []components.Hint{
			{Key: "type", Label: "to filter"},
			{Key: "enter", Label: "keep"},
			{Key: "esc", Label: "clear"},
		}
	}
	if m.detail != nil {
		return []components.Hint{
			{Key: "esc", Label: "back"},
			{Key: "tab", Label: "view"},
			{Key: "q", Label: "quit"},
		}
	}

	common := []components.Hint{
		{Key: "tab", Label: "view"},
		{Key: "/", Label: "search"},
		{Key: "r", Label: "refresh"},
		{Key: "?", Label: "help"},
		{Key: "q", Label: "quit"},
	}
	var view []components.Hint
	switch m.active {
	case ViewPS2Sources, ViewPS1Sources:
		view = []components.Hint{
			{Key: "↑↓", Label: "move"},
			{Key: "space", Label: "select"},
			{Key: "a", Label: "all"},
			{Key: "i", Label: "install"},
			{Key: "enter", Label: "details"},
		}
	case ViewInstalled:
		view = []components.Hint{
			{Key: "↑↓", Label: "move"},
			{Key: "space", Label: "select"},
			{Key: "d", Label: "remove"},
			{Key: "a", Label: "artwork"},
			{Key: "f", Label: "filter"},
			{Key: "enter", Label: "details"},
		}
	case ViewAssets:
		view = []components.Hint{
			{Key: "↑↓", Label: "move"},
			{Key: "space", Label: "select"},
			{Key: "a", Label: "sync"},
			{Key: "A", Label: "sync all missing"},
			{Key: "f", Label: "incomplete only"},
		}
	case ViewQueue:
		view = []components.Hint{
			{Key: "c", Label: "cancel"},
			{Key: "R", Label: "retry failed"},
			{Key: "x", Label: "clear finished"},
		}
	case ViewDrive:
		view = []components.Hint{
			{Key: "p", Label: "partitions"},
			{Key: "s", Label: "re-check PS1"},
		}
	case ViewSettings:
		view = []components.Hint{
			{Key: "↑↓", Label: "move"},
			{Key: "enter", Label: "change"},
			{Key: "s", Label: "save"},
			{Key: "u", Label: "undo"},
		}
	}
	return append(view, common...)
}
