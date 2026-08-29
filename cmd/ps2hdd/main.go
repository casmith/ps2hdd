// Command ps2hdd manages a PlayStation 1/2 HDD from Linux.
//
// Run with no arguments it opens a terminal interface; every operation is also
// available as a subcommand so it can be scripted.
package main

import (
	"os"

	"golang.org/x/term"

	"github.com/casmith/ps2hdd/internal/cli"
	"github.com/casmith/ps2hdd/internal/tui"
)

// version is overridden at build time with
// -ldflags "-X main.version=$(git describe --tags)".
var version = "dev"

func main() {
	cli.Version = version
	// Colour is for humans reading a terminal, not for pipes and log files.
	cli.SetColor(term.IsTerminal(int(os.Stdout.Fd())))

	root, env := cli.NewRootCommand(func(e *cli.Env) error { return tui.Run(e) })
	os.Exit(cli.Execute(root, env))
}
