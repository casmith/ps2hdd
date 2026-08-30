package cli

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// DoctorReport is the machine-readable form of `ps2hdd doctor`.
type DoctorReport struct {
	Go      string           `json:"go"`
	Version string           `json:"version"`
	Config  string           `json:"config"`
	Log     string           `json:"log"`
	Tools   []app.ToolStatus `json:"tools"`
	// Distro is the detected host distribution, and Remedies say how to
	// obtain each missing tool on it. A diagnosis without a next step is
	// half a diagnosis.
	Distro    app.Distro            `json:"distro"`
	Remedies  []app.ToolRemedy      `json:"remedies,omitempty"`
	ToolsHint string                `json:"tools_hint,omitempty"`
	Device    DoctorDevice          `json:"device"`
	Sources   []app.SourceDirStatus `json:"sources"`
	Provider  DoctorProvider        `json:"asset_provider"`
	PS1       ps1.Readiness         `json:"ps1"`
	// CrossCheck compares the native APA reader against hdl_dump. It is the
	// check that says whether the foundation everything else stands on is
	// sound, so it is part of the routine report rather than a separate tool.
	CrossCheck app.CrossCheck `json:"crosscheck"`
	// Problems lists the findings that need action, in the order to act on them.
	Problems []string `json:"problems,omitempty"`
}

// DoctorDevice is the drive half of the report.
type DoctorDevice struct {
	Configured string `json:"configured"`
	OK         bool   `json:"ok"`
	Reason     string `json:"reason,omitempty"`
	Path       string `json:"path,omitempty"`
	Model      string `json:"model,omitempty"`
	SizeBytes  int64  `json:"size_bytes,omitempty"`
	APA        bool   `json:"apa"`
	HasOPL     bool   `json:"has_opl"`
	HasPOPS    bool   `json:"has_pops"`
	HasCommon  bool   `json:"has_common"`
	PS2Games   int    `json:"ps2_games"`
	PS1Games   int    `json:"ps1_games"`
}

// DoctorProvider is the artwork half.
type DoctorProvider struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Reason string `json:"reason,omitempty"`
	// Enabled is every art slot the configuration asks for; Unavailable is
	// the subset this provider cannot supply for anyone. A non-empty
	// Unavailable is a setup problem, not a per-game one.
	Enabled     []model.AssetType `json:"enabled_slots,omitempty"`
	Unavailable []model.AssetType `json:"unavailable_slots,omitempty"`
}

func newDoctorCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything ps2hdd needs is in place",
		Long: `Report on the Go runtime, the external tools, the configured HDD, the source
directories, the artwork provider and PS1 support, and say what to do about
anything that is not working.

Reading the library needs no external tools at all: ps2hdd parses the APA
partition table itself. hdl_dump is only needed to install and remove PS2
games, and pfsfuse only to reach +OPL and __.POPS.

When hdl_dump is installed and can read the drive, the installed library is
also read twice -- natively and through hdl_dump -- and the two are compared.
That native parse is what every listing and every install decision rests on, so
a disagreement is reported as a problem and no write should follow.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rep := buildDoctorReport(cmd.Context(), env)
			if env.JSON {
				if err := env.emitJSON(rep); err != nil {
					return err
				}
				if len(rep.Problems) > 0 {
					return fmt.Errorf("%d problem(s) found", len(rep.Problems))
				}
				return nil
			}
			renderDoctor(env, rep)
			if len(rep.Problems) > 0 {
				return fmt.Errorf("%d problem(s) found", len(rep.Problems))
			}
			return nil
		},
	}
}

func buildDoctorReport(ctx context.Context, env *Env) DoctorReport {
	rep := DoctorReport{
		Go:      runtime.Version(),
		Version: Version,
		Config:  env.Config.Path(),
	}
	if p, err := logging.Path(); err == nil {
		rep.Log = p
	}

	rep.Tools = env.Svc.Tools()
	rep.Distro = app.DetectDistro()
	for _, t := range rep.Tools {
		if t.Present {
			continue
		}
		if t.Required {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("%s is not installed; it is needed for %s.", t.Name, t.Purpose))
		} else {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("%s is not installed, so %s will not work. Reading the library still does.", t.Name, t.Purpose))
		}
		rep.Remedies = append(rep.Remedies, app.Remedy(t.Name, rep.Distro))
	}
	if len(rep.Remedies) > 0 {
		rep.ToolsHint = app.InstallHint()
	}

	rep.Device.Configured = env.Config.Device
	switch {
	case env.Config.Device == "":
		rep.Device.Reason = "no device configured"
		rep.Problems = append(rep.Problems, "No PS2 HDD is configured. Run `ps2hdd detect --configure`.")
	default:
		st, err := env.Svc.Status(ctx)
		if err != nil {
			rep.Device.Reason = err.Error()
			rep.Problems = append(rep.Problems, "The configured HDD is unusable: "+firstLine(err.Error()))
		} else {
			rep.Device.OK = true
			rep.Device.Path = st.DevicePath
			rep.Device.Model = st.Model
			rep.Device.SizeBytes = st.SizeBytes
			rep.Device.APA = st.APADetected
			rep.Device.HasOPL = st.HasOPL
			rep.Device.HasPOPS = st.HasPOPS
			rep.Device.HasCommon = st.HasCommon
			rep.Device.PS2Games = st.PS2Games
			rep.Device.PS1Games = st.PS1Games
			if !st.HasOPL {
				rep.Problems = append(rep.Problems,
					fmt.Sprintf("The %s partition is missing, so artwork and per-game configuration have nowhere to live.", drive.PartitionOPL))
			}
		}
	}

	rep.Sources = env.Svc.SourceDirs()
	for _, s := range rep.Sources {
		if s.OK || s.Path == "" {
			continue
		}
		rep.Problems = append(rep.Problems,
			fmt.Sprintf("The %s source directory %s is unusable: %s.", s.Platform.Label(), s.Path, s.Reason))
	}

	if p, err := env.Svc.AssetProvider(); err != nil {
		rep.Provider.Reason = err.Error()
		rep.Problems = append(rep.Problems, "The artwork provider is misconfigured: "+err.Error())
	} else {
		rep.Provider.Name = p.Name()
		rep.Provider.Enabled = env.Config.WantedAssets()
		_, rep.Provider.Unavailable = env.Svc.WantedAndUnavailable()
		if len(rep.Provider.Unavailable) > 0 {
			names := make([]string, 0, len(rep.Provider.Unavailable))
			for _, t := range rep.Provider.Unavailable {
				names = append(names, string(t))
			}
			rep.Problems = append(rep.Problems, fmt.Sprintf(
				"%d of %d enabled artwork slots (%s) cannot be supplied by the %s provider, so they will never be filled. Turn them off, or switch to a provider that has them.",
				len(rep.Provider.Unavailable), len(rep.Provider.Enabled),
				strings.Join(names, ", "), p.Name()))
		}
		if err := p.Check(ctx); err != nil {
			rep.Provider.Reason = err.Error()
			rep.Problems = append(rep.Problems, "The artwork provider is unreachable: "+firstLine(err.Error()))
		} else {
			rep.Provider.OK = true
		}
	}

	if rep.Device.OK {
		if ready, err := env.Svc.PS1Readiness(ctx); err == nil {
			rep.PS1 = ready
			if !ready.Ready() {
				rep.Problems = append(rep.Problems, ready.Explain()...)
			}
		}
		if cc, err := env.Svc.CrossCheckReader(ctx); err == nil {
			rep.CrossCheck = cc
			for _, d := range cc.Disagreements {
				rep.Problems = append(rep.Problems,
					"ps2hdd and hdl_dump disagree about the library: "+d+". Do not write to this disk.")
			}
		}
	}
	return rep
}

func renderDoctor(env *Env, rep DoctorReport) {
	section(env.Out, "Environment")
	kv(env.Out, [][2]string{
		{"ps2hdd", rep.Version},
		{"Go runtime", rep.Go},
		{"Config", rep.Config},
		{"Log", rep.Log},
	})

	section(env.Out, "External tools")
	t := newTable(env.Out)
	fmt.Fprintln(t, bold("TOOL\tSTATUS\tNEEDED FOR"))
	for _, tool := range rep.Tools {
		status := green("OK")
		if !tool.Present {
			status = amber("missing")
			if tool.Required {
				status = red("MISSING")
			}
		}
		fmt.Fprintf(t, "%s\t%s\t%s\n", tool.Name, status, dim(tool.Purpose))
	}
	t.Flush()

	// Printed straight after the tool table, where the missing ones are, so
	// the fix is next to the finding rather than in a file the reader has to
	// know exists.
	if len(rep.Remedies) > 0 {
		title := "How to install what is missing"
		if rep.Distro.Known() {
			title += " (" + rep.Distro.Name + ")"
		}
		section(env.Out, title)
		for _, r := range rep.Remedies {
			env.printf("  %s\n", bold(r.Tool))
			if r.Note != "" {
				env.printf("    %s\n", dim(r.Note))
			}
			for _, c := range r.Commands {
				env.printf("    %s %s\n", dim("$"), c)
			}
			if len(r.Commands) == 0 && r.Note == "" {
				env.printf("    %s\n", dim("see docs/dependencies.md"))
			}
		}
		if rep.ToolsHint != "" {
			env.printf("\n  %s %s\n", dim("note:"), rep.ToolsHint)
		}
	}

	section(env.Out, "PS2 HDD")
	if !rep.Device.OK {
		if rep.Device.Configured == "" {
			kv(env.Out, [][2]string{
				{"Device", dim("not configured")},
				{"Next step", "run `ps2hdd detect --configure`"},
			})
		} else {
			kv(env.Out, [][2]string{
				{"Device", rep.Device.Configured},
				{"Status", red("unusable")},
				{"Reason", firstLine(rep.Device.Reason)},
			})
		}
	} else {
		kv(env.Out, [][2]string{
			{"Device", rep.Device.Configured},
			{"Model", orDash(rep.Device.Model)},
			{"Capacity", model.HumanSize(rep.Device.SizeBytes)},
			{"APA", boolLabel(rep.Device.APA, "OK", "not detected")},
			{drive.PartitionOPL, boolLabel(rep.Device.HasOPL, "OK", "missing")},
			{ps1.POPSPartition, boolLabel(rep.Device.HasPOPS, "OK", "missing")},
			{ps1.CommonPartition, boolLabel(rep.Device.HasCommon, "OK", "missing")},
			{"PS2 games", fmt.Sprintf("%d", rep.Device.PS2Games)},
			{"PS1 games", fmt.Sprintf("%d", rep.Device.PS1Games)},
		})
	}

	if rep.Device.OK {
		section(env.Out, "Reader cross-check")
		// Printed whether or not it ran. An omitted section would let "could
		// not check" read as "checked and fine", which is the one conclusion
		// this must never invite.
		switch {
		case rep.CrossCheck.Agree():
			kv(env.Out, [][2]string{
				{"ps2hdd", fmt.Sprintf("%d game(s)", rep.CrossCheck.NativeGames)},
				{"hdl_dump", fmt.Sprintf("%d game(s)", rep.CrossCheck.ReferenceGames)},
				{"Agreement", green("OK")},
			})
		case rep.CrossCheck.Ran:
			kv(env.Out, [][2]string{
				{"ps2hdd", fmt.Sprintf("%d game(s)", rep.CrossCheck.NativeGames)},
				{"hdl_dump", fmt.Sprintf("%d game(s)", rep.CrossCheck.ReferenceGames)},
				{"Agreement", red("MISMATCH")},
			})
			for _, d := range rep.CrossCheck.Disagreements {
				env.printf("  %s %s\n", red("-"), d)
			}
		default:
			kv(env.Out, [][2]string{
				{"Agreement", dim("not checked")},
				{"Reason", firstLine(rep.CrossCheck.Unavailable)},
			})
		}
	}

	section(env.Out, "Support")
	ps2Ready := rep.Device.OK && rep.Device.APA
	kv(env.Out, [][2]string{
		{"PS2", boolLabel(ps2Ready, "READY", "NOT READY")},
		{"PS1", colorStatus(rep.PS1.Status())},
	})

	section(env.Out, "Source directories")
	for _, s := range rep.Sources {
		if s.Path == "" {
			env.printf("  %-4s %s\n", s.Platform.Label(), dim("not configured"))
			continue
		}
		if s.OK {
			env.printf("  %-4s %s  %s\n", s.Platform.Label(), green("OK"), s.Path)
			continue
		}
		env.printf("  %-4s %s  %s\n", s.Platform.Label(), amber(s.Reason), s.Path)
	}

	section(env.Out, "Artwork provider")
	if len(rep.Provider.Enabled) > 0 {
		names := func(ts []model.AssetType) string {
			out := make([]string, 0, len(ts))
			for _, t := range ts {
				out = append(out, string(t))
			}
			return strings.Join(out, ", ")
		}
		kv(env.Out, [][2]string{
			{"Slots enabled", names(rep.Provider.Enabled)},
		})
		if len(rep.Provider.Unavailable) > 0 {
			kv(env.Out, [][2]string{
				{"Not supplied", amber(names(rep.Provider.Unavailable))},
			})
		}
	}
	status := green("OK")
	if !rep.Provider.OK {
		status = amber(firstLine(rep.Provider.Reason))
	}
	kv(env.Out, [][2]string{
		{"Provider", orDash(rep.Provider.Name)},
		{"Status", status},
	})

	if len(rep.Problems) == 0 {
		env.printf("\n%s\n", green("Everything checks out."))
		return
	}
	section(env.Out, fmt.Sprintf("Problems (%d)", len(rep.Problems)))
	for i, p := range rep.Problems {
		env.printf("  %d. %s\n", i+1, p)
	}
}

func firstLine(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
