package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

// maxListedProblems caps the unidentified-files section.
//
// A large library produces a lot of them -- 135 in one real PS1 collection,
// most of them the same handful of causes -- and printing every one buries the
// listing the user actually asked for. The count in the heading is always the
// true total, so nothing is hidden, only deferred to --json.
const maxListedProblems = 10

func newListCommand(env *Env) *cobra.Command {
	var (
		onlyPS1    bool
		onlyPS2    bool
		installed  bool
		available  bool
		missingArt bool
		multiDisc  bool
		search     string
		noArtwork  bool
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the unified PS1/PS2 library",
		Long: `List installed games and games available in the configured source
directories, reconciled into one view.

Source directories are browsing locations, never a record of what is installed.
Only the HDD decides that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if onlyPS1 && onlyPS2 {
				return fmt.Errorf("--ps1 and --ps2 are mutually exclusive")
			}
			if installed && available {
				return fmt.Errorf("--installed and --available are mutually exclusive")
			}

			c, warnings, err := env.Svc.Catalog(cmd.Context())
			if err != nil {
				return err
			}
			f := catalog.Filter{
				Installed:    installed,
				NotInstalled: available,
				MissingAsset: missingArt,
				MultiDisc:    multiDisc,
				Search:       search,
			}
			switch {
			case onlyPS1:
				f.Platform = model.PlatformPS1
			case onlyPS2:
				f.Platform = model.PlatformPS2
			}
			entries := c.Apply(f)

			if env.JSON {
				return env.emitJSON(struct {
					Entries  []catalog.CatalogEntry `json:"entries"`
					Problems []catalog.ScanProblem  `json:"problems,omitempty"`
					Warnings []string               `json:"warnings,omitempty"`
				}{entries, c.Problems, errStrings(warnings)})
			}

			for _, w := range warnings {
				env.warnf("%s %v\n", amber("warning:"), w)
			}
			renderGames(env.Out, entries, !noArtwork)

			if len(entries) > 0 {
				// Counted over what was shown, not over the catalog: under a
				// filter the two are different numbers and only one of them
				// describes the list above.
				inst, avail, missing := catalog.Count(entries)
				parts := []string{fmt.Sprintf("%d shown", len(entries))}
				if inst > 0 {
					parts = append(parts, fmt.Sprintf("%d installed", inst))
				}
				if avail > 0 {
					parts = append(parts, fmt.Sprintf("%d available", avail))
				}
				if missing > 0 {
					parts = append(parts, fmt.Sprintf("%d missing artwork", missing))
				}
				env.printf("\n%s\n", strings.Join(parts, "; "))
			}
			// Source problems belong to a listing about sources. Under
			// --installed the question was what is on the HDD, and a wall of
			// files that failed to identify in a directory somewhere else is
			// not an answer to it -- on a real library that is over a hundred
			// lines of it.
			if len(c.Problems) > 0 && !installed {
				section(env.Out, fmt.Sprintf("Unidentified source files (%d)", len(c.Problems)))
				shown := c.Problems
				if len(shown) > maxListedProblems {
					shown = shown[:maxListedProblems]
				}
				for _, p := range shown {
					env.printf("  %s\n    %s\n", p.Path, dim(p.Reason))
				}
				if n := len(c.Problems) - len(shown); n > 0 {
					env.printf("  %s\n", dim(fmt.Sprintf("… and %d more; --json lists them all", n)))
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&onlyPS1, "ps1", false, "only PlayStation 1 titles")
	f.BoolVar(&onlyPS2, "ps2", false, "only PlayStation 2 titles")
	f.BoolVar(&installed, "installed", false, "only titles installed on the HDD")
	f.BoolVar(&available, "available", false, "only titles not yet installed")
	f.BoolVar(&missingArt, "missing-art", false, "only installed titles with missing artwork")
	f.BoolVar(&multiDisc, "multi-disc", false, "only multi-disc titles")
	f.StringVar(&search, "search", "", "substring match against the title or game ID")
	f.BoolVar(&noArtwork, "no-artwork", false, "skip the artwork column (avoids mounting +OPL)")
	return cmd
}

func newInfoCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "info <game-id|title|path>",
		Short: "Show everything known about one title",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]

			// A path is inspected directly, so `info` works on an image that
			// has never been near the HDD.
			if looksLikePath(query) {
				g, err := env.Svc.InspectSource(query)
				if err != nil {
					return err
				}
				if env.JSON {
					return env.emitJSON(g)
				}
				renderGameDetail(env, catalog.CatalogEntry{Game: g, AvailableInSource: true})
				return nil
			}

			c, warnings, err := env.Svc.Catalog(cmd.Context())
			if err != nil {
				return err
			}
			matches := c.Find(query)
			switch len(matches) {
			case 0:
				return fmt.Errorf("no game matches %q", query)
			case 1:
			default:
				// Ambiguity is reported, never resolved on the user's behalf.
				var b strings.Builder
				fmt.Fprintf(&b, "%q matches %d games:", query, len(matches))
				for _, m := range matches {
					fmt.Fprintf(&b, "\n  %-4s %-14s %s", m.Platform.Label(), m.GameID, m.Title)
				}
				b.WriteString("\n\nName one of them by its game ID")
				return errors.New(b.String())
			}
			if env.JSON {
				return env.emitJSON(matches[0])
			}
			for _, w := range warnings {
				env.warnf("%s %v\n", amber("warning:"), w)
			}
			renderGameDetail(env, matches[0])
			return nil
		},
	}
	return cmd
}

func renderGameDetail(env *Env, e catalog.CatalogEntry) {
	env.printf("\n%s\n", bold(e.Title))
	pairs := [][2]string{
		{"Platform", platformName(e.Platform)},
		{"ID", e.GameID},
		{"Size", model.HumanSize(e.SizeBytes)},
		{"Installed", yesNo(e.Installed)},
	}
	if e.Media != model.MediaUnknown {
		pairs = append(pairs, [2]string{"Media", strings.ToUpper(string(e.Media))})
	}
	if e.StorageBackend != "" {
		pairs = append(pairs, [2]string{"Stored as", storageName(e.StorageBackend)})
	}
	if e.PartitionName != "" {
		pairs = append(pairs, [2]string{"On HDD", e.PartitionName})
	}
	if e.SourcePath != "" {
		pairs = append(pairs, [2]string{"Source", e.SourcePath})
	}
	kv(env.Out, pairs)

	if e.IsMultiDisc() || (e.Platform == model.PlatformPS1 && len(e.Discs) > 0) {
		section(env.Out, fmt.Sprintf("Discs (%d)", e.DiscCount()))
		t := newTable(env.Out)
		fmt.Fprintln(t, bold("DISC\tID\tSIZE\tLOCATION"))
		for _, d := range e.Discs {
			loc := d.SourcePath
			if d.InstalledName != "" {
				loc = d.InstalledName
			}
			fmt.Fprintf(t, "%d\t%s\t%s\t%s\n", d.Number, d.GameID, model.HumanSize(d.SizeBytes), loc)
		}
		t.Flush()
	}

	if e.Installed {
		section(env.Out, "Artwork")
		if !e.AssetsKnown {
			env.printf("  %s\n", dim("unknown (+OPL could not be read)"))
		} else if len(e.MissingAssets) == 0 {
			env.printf("  %s\n", green("complete"))
		} else {
			for _, t := range e.MissingAssets {
				desc := ""
				if d, ok := model.Dimensions(e.Platform, t); ok {
					desc = fmt.Sprintf(" (%dx%d)", d.Width, d.Height)
				}
				env.printf("  %-6s %s%s\n", t, amber("missing"), dim(desc))
			}
		}
		// Slots the provider cannot supply are listed apart from the gaps, in
		// a calmer colour: nothing the user does will fill them.
		for _, t := range e.UnavailableAssets {
			env.printf("  %-6s %s\n", t, dim("not supplied by "+env.Config.Assets.Provider))
		}
	}
}

func platformName(p model.Platform) string {
	switch p {
	case model.PlatformPS1:
		return "PlayStation 1"
	case model.PlatformPS2:
		return "PlayStation 2"
	}
	return string(p)
}

func storageName(b string) string {
	switch b {
	case model.BackendHDL:
		return "HDLoader partition"
	case model.BackendPOPS:
		return "POPS virtual CD (VCD)"
	}
	return b
}

func yesNo(b bool) string {
	if b {
		return green("yes")
	}
	return "no"
}

func looksLikePath(s string) bool {
	return strings.ContainsAny(s, "/\\") || strings.HasPrefix(s, ".") ||
		strings.HasSuffix(strings.ToLower(s), ".iso") || strings.HasSuffix(strings.ToLower(s), ".cue")
}

func errStrings(errs []error) []string {
	if len(errs) == 0 {
		return nil
	}
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		out = append(out, e.Error())
	}
	return out
}
