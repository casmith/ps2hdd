package tui

import (
	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
)

// EnvAdapter is the handle the CLI passes to Run.
//
// It exists so internal/cli does not have to expose its Env type and internal/
// tui does not have to import internal/cli, which would make the dependency
// cycle the layering is meant to avoid. The CLI satisfies it structurally.
type EnvAdapter interface {
	Services() *app.Services
	ConfigValue() config.Config
}
