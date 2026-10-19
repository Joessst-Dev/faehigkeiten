package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/update"
)

func newInstallCommand() *cobra.Command {
	var (
		scope    scopeFlags
		agents   []string
		track    string
		ref      string
		all      bool
		force    bool
		listOnly bool
		versions bool
	)
	cmd := &cobra.Command{
		Use:   "install <source> [skill[@version]...]",
		Short: "Install skills from a repository",
		Long: `Install skills from a source: "owner/repo" (GitHub), a git URL or a local directory.
Select skills by name or path, or use --all.

Append @version to a skill to install an older version from the history of
its SKILL.md (see --versions). Such installs are pinned: check still reports
newer versions, but update leaves them alone. @latest installs the newest
version and removes the pin.

Tracking modes:
  version  compare the version in SKILL.md (default for versioned skills)
  hash     compare a content hash of the skill directory
  none     never check for updates (default for unversioned skills)`,
		Example: `  faehigkeiten install anthropics/skills pdf docx
  faehigkeiten install anthropics/skills --list
  faehigkeiten install anthropics/skills pdf --versions
  faehigkeiten install anthropics/skills pdf@1.2.0
  faehigkeiten install ./my-skills my-skill --global --agent claude-code,codex --track hash`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			co, skills, err := a.Load(cmd.Context(), args[0], ref, true)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if listOnly {
				printSkills(out, skills)
				return nil
			}
			names, pins, pinned := parseSelectors(args[1:])
			if versions {
				if len(names) != 1 || pins[0] != "" {
					return errors.New("--versions needs exactly one skill name")
				}
				selected, err := app.Select(skills, names)
				if err != nil {
					return err
				}
				// The resolved ref shares the history installs use.
				vs, err := a.Versions(cmd.Context(), co.URL, co.Ref, selected[0].Path, true)
				if err != nil {
					return err
				}
				printVersions(out, vs)
				return nil
			}
			selected := skills
			if !all {
				if len(args) == 1 {
					return fmt.Errorf("name the skills to install or pass --all (use --list to see %d available skills)", len(skills))
				}
				if selected, err = app.Select(skills, names); err != nil {
					return err
				}
			} else {
				if pinned {
					return errors.New("--all cannot be combined with @version")
				}
				if pins, err = allPins(skills, names, pins); err != nil {
					return err
				}
			}
			tracking, err := parseTrack(track)
			if err != nil {
				return err
			}
			t, err := scope.target(a)
			if err != nil {
				return err
			}
			ids := splitList(agents)
			if len(ids) == 0 {
				ids = a.DefaultAgents(t)
			}
			targets, err := a.Agents.Lookup(ids)
			if err != nil {
				return err
			}
			in, err := a.Installer(t)
			if err != nil {
				return err
			}
			refreshed := false
			for i, s := range selected {
				v := pins[i]
				req, err := a.Prepare(cmd.Context(), in, co, s, app.Selector{Version: v}, !refreshed)
				if err != nil {
					return err
				}
				// The history only needs updating once per source, which
				// resolving a version does; keeping a pin does not.
				refreshed = refreshed || (v != "" && v != app.Latest)
				req.Agents, req.Tracking, req.Force = targets, tracking, force
				applyTracking(cmd.ErrOrStderr(), &req, s, v)
				e, err := in.Install(req)
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "installed %s (%s) for %s\n", e.Name, install.Describe(e), strings.Join(e.Agents, ", "))
			}
			return in.Lock.Save()
		},
	}
	scope.register(cmd)
	cmd.Flags().StringSliceVarP(&agents, "agent", "a", nil, "target agents (comma separated, see `faehigkeiten agents`)")
	cmd.Flags().StringVarP(&track, "track", "t", "", "tracking mode: version, hash or none")
	cmd.Flags().StringVar(&ref, "ref", "", "branch or tag to install from")
	cmd.Flags().BoolVar(&all, "all", false, "install every skill in the source")
	cmd.Flags().BoolVarP(&force, "force", "f", false, "overwrite existing skill directories")
	cmd.Flags().BoolVarP(&listOnly, "list", "l", false, "list skills in the source without installing")
	cmd.Flags().BoolVar(&versions, "versions", false, "list the versions of a skill without installing")
	cmd.MarkFlagsMutuallyExclusive("all", "versions")
	cmd.MarkFlagsMutuallyExclusive("list", "versions")
	return cmd
}

// parseSelectors splits skill[@version] arguments into names and version
// selectors, and reports whether any selects a specific version.
func parseSelectors(args []string) (names, pins []string, pinned bool) {
	names = make([]string, len(args))
	pins = make([]string, len(args))
	for i, arg := range args {
		names[i], pins[i] = app.SplitVersion(arg)
		pinned = pinned || (pins[i] != "" && pins[i] != app.Latest)
	}
	return names, pins, pinned
}

// parseTrack parses an optional --track value; empty means not given.
func parseTrack(s string) (lock.Tracking, error) {
	if s == "" {
		return "", nil
	}
	return lock.ParseTracking(s)
}

// applyTracking falls back to hash tracking in req when s has no version and
// tells the user when a pin or that fallback overrides the requested
// tracking; v is the version selector of s.
func applyTracking(w io.Writer, req *install.Request, s skill.Skill, v string) {
	if req.Pinned {
		if v == "" {
			fmt.Fprintf(w, "%s is %s and stays there (install %s@latest to unpin it)\n", s.Name, install.PinLabel(req.Skill.Version), s.Name)
		}
		if req.Tracking != "" && req.Tracking != lock.TrackVersion {
			fmt.Fprintf(w, "%s is pinned, using version tracking\n", s.Name)
		}
		return
	}
	if req.Tracking == lock.TrackVersion && !s.HasVersion() {
		req.Tracking = lock.TrackHash
		fmt.Fprintf(w, "%s has no version, using hash tracking\n", s.Name)
	}
}

// newTable returns a tabwriter for a table on w with its header written.
func newTable(w io.Writer, header string) *tabwriter.Writer {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, header)
	return tw
}

func printSkills(w io.Writer, skills []skill.Skill) {
	tw := newTable(w, "NAME\tVERSION\tLICENSE\tPATH\tDESCRIPTION")
	for _, s := range skills {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Name, dash(s.Version), licenseColumn(s.License.Label()), s.Path, truncate(s.Description, 70))
	}
	tw.Flush()
}

// allPins maps the version selectors given with --all (only @latest is
// allowed there) to every skill, so naming pdf@latest unpins pdf.
func allPins(skills []skill.Skill, names, versions []string) ([]string, error) {
	latest := map[string]bool{}
	for i, n := range names {
		if versions[i] != app.Latest {
			continue
		}
		named, err := app.Select(skills, []string{n})
		if err != nil {
			return nil, err
		}
		latest[named[0].Path] = true
	}
	out := make([]string, len(skills))
	for i, s := range skills {
		if latest[s.Path] {
			out[i] = app.Latest
		}
	}
	return out, nil
}

// printVersions lists versions and marks the one at the tip of the ref.
func printVersions(w io.Writer, vs []source.Version) {
	tw := newTable(w, "VERSION\tCOMMIT\tDATE")
	for _, v := range vs {
		latest := ""
		if v.Latest {
			latest = "\t(latest)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s%s\n", v.Version, source.ShortCommit(v.Commit), v.Date.Format(time.DateOnly), latest)
	}
	tw.Flush()
}

func licenseColumn(label string) string { return dash(truncate(label, 20)) }

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func newListCommand() *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List installed skills",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, in, err := scope.open()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			found := in.Unmanaged()
			if len(in.Lock.Skills) == 0 && len(found) == 0 {
				fmt.Fprintln(out, "no skills installed")
				return nil
			}
			tw := newTable(out, "NAME\tTRACKING\tLICENSE\tAGENTS\tSOURCE")
			for _, e := range in.Lock.Skills {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", e.Name, install.Describe(e), licenseColumn(install.License(e).Label()), strings.Join(e.Agents, ","), source.DisplayName(e.Source))
			}
			for _, f := range found {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", f.Name, "not managed", licenseColumn(f.License().Label()), strings.Join(f.Agents, ","), "-")
			}
			return tw.Flush()
		},
	}
	scope.register(cmd)
	return cmd
}

func newLicenseCommand() *cobra.Command {
	var (
		scope scopeFlags
		ref   string
	)
	cmd := &cobra.Command{
		Use:   "license [source] <skill>",
		Short: "Show the license of a skill",
		Long: `Print the license of an installed skill, or with a source, of a skill in
that source. The license is the license file of the skill, or of its
repository when the skill has none, or else the license field of its
SKILL.md.`,
		Example: `  faehigkeiten license pdf
  faehigkeiten license anthropics/skills pdf`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			var text string
			if len(args) == 2 {
				if scope.global || scope.project != "" {
					return errors.New("--global and --project select installed skills; they cannot be used with a source")
				}
				if text, err = a.SkillLicenseText(cmd.Context(), args[0], ref, args[1]); err != nil {
					return err
				}
			} else {
				if ref != "" {
					return errors.New("--ref needs a source")
				}
				in, err := scope.installer(a)
				if err != nil {
					return err
				}
				name := args[0]
				if e, ok := in.Lock.Get(name); ok {
					var note string
					if text, note, err = a.EntryLicenseText(cmd.Context(), in, e); note != "" {
						fmt.Fprintln(cmd.ErrOrStderr(), "note: "+note)
					}
				} else {
					found := in.Unmanaged()
					i := slices.IndexFunc(found, func(f install.Found) bool { return f.Name == name })
					if i < 0 {
						return errNotInstalled(name)
					}
					text, err = app.FoundLicenseText(found[i])
				}
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), text)
			return err
		},
	}
	scope.register(cmd)
	cmd.Flags().StringVar(&ref, "ref", "", "branch or tag of the source")
	return cmd
}

func newCheckCommand() *cobra.Command {
	var (
		scope    scopeFlags
		exitCode bool
	)
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check tracked skills for updates",
		Long: `Check tracked skills for updates. Skills whose installed files were edited
locally are flagged, because updating them overwrites those edits.

Exits with status 1 if any skill could not be checked.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, in, err := scope.open()
			if err != nil {
				return err
			}
			sts := update.Check(cmd.Context(), a.Sources, in)
			available, failed := printStatuses(cmd.OutOrStdout(), sts)
			switch {
			case failed > 0:
				return ExitError{Code: 1}
			case available > 0 && exitCode:
				return ExitError{Code: 10}
			}
			return nil
		},
	}
	scope.register(cmd)
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "exit with status 10 when updates are available")
	return cmd
}

// printStatuses renders update statuses and returns how many updates are
// available and how many checks failed.
func printStatuses(w io.Writer, sts []update.Status) (available, failed int) {
	if len(sts) == 0 {
		fmt.Fprintln(w, "no tracked skills")
		return 0, 0
	}
	tw := newTable(w, "NAME\tTRACKING\tINSTALLED\tLATEST\tSTATUS")
	for _, s := range sts {
		var status string
		if s.Err != nil {
			status = "error: " + s.Err.Error()
			failed++
		} else {
			status = describeStatus(s)
			// Pinned skills are not updated, so they don't count as available.
			if s.Updatable() {
				available++
			}
		}
		cur, latest := s.Current, s.Latest
		if s.Entry.Tracking == lock.TrackHash {
			cur, latest = skill.ShortHash(cur), skill.ShortHash(latest)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Entry.Name, s.Entry.Tracking, dash(cur), dash(latest), status)
	}
	tw.Flush()
	return available, failed
}

// describeStatus renders the notes of a status as "first (rest, …)".
func describeStatus(s update.Status) string {
	notes := s.Notes()
	if s.Updatable() && s.Modified {
		notes = append(notes, "updating overwrites your changes")
	}
	if len(notes) == 1 {
		return notes[0]
	}
	return notes[0] + " (" + strings.Join(notes[1:], ", ") + ")"
}

// errNotInstalled reports a skill missing from the lockfile of the target.
func errNotInstalled(name string) error {
	return fmt.Errorf("skill %s is not installed in this target", name)
}

func newUpdateCommand() *cobra.Command {
	var (
		scope scopeFlags
		force bool
		unpin bool
	)
	cmd := &cobra.Command{
		Use:   "update [skill...]",
		Short: "Update tracked skills (all with available updates by default)",
		Long: `Update tracked skills. Skills whose installed files were edited locally are
skipped with a warning unless --force is given, which overwrites the edits.

Skills installed at a specific version are pinned and skipped. --unpin removes
the pin of the named skills and updates them to the latest version.

Exits with status 1 if any skill could not be checked or updated.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			a, in, err := scope.open()
			if err != nil {
				return err
			}
			if unpin && len(args) == 0 {
				return errors.New("--unpin needs the names of the skills to unpin")
			}
			for _, name := range args {
				if _, ok := in.Lock.Get(name); !ok {
					return errNotInstalled(name)
				}
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			updated, failed, unpinned := 0, 0, 0
			for _, s := range update.Check(cmd.Context(), a.Sources, in) {
				if len(args) > 0 && !slices.Contains(args, s.Entry.Name) {
					continue
				}
				switch updateOne(out, errOut, in, s, unpin, force) {
				case resultUpdated:
					updated++
				case resultUnpinned:
					unpinned++
				case resultFailed:
					failed++
				}
			}
			if updated+unpinned > 0 {
				if err := in.Lock.Save(); err != nil {
					return err
				}
			}
			if updated == 0 && unpinned == 0 && failed == 0 {
				fmt.Fprintln(out, "nothing to update")
			}
			if failed > 0 {
				return ExitError{Code: 1}
			}
			return nil
		},
	}
	scope.register(cmd)
	cmd.Flags().BoolVarP(&force, "force", "f", false, "also update skills with local changes, overwriting them")
	cmd.Flags().BoolVar(&unpin, "unpin", false, "unpin the named skills and update them to the latest version")
	return cmd
}

// updateResult is what updateOne did with a skill.
type updateResult int

const (
	resultSkipped updateResult = iota
	resultUpdated
	resultUnpinned
	resultFailed
)

// updateOne updates or unpins the skill of s as the flags of the update
// command ask, reporting on out and errOut.
func updateOne(out, errOut io.Writer, in *install.Installer, s update.Status, unpin, force bool) updateResult {
	if s.Err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", s.Entry.Name, s.Err)
		return resultFailed
	}
	if s.Entry.Pinned && !unpin {
		if s.Available {
			fmt.Fprintf(errOut, "skipping %s: %s (use --unpin to update it)\n", s.Entry.Name, install.PinLabel(s.Entry.Version))
		}
		return resultSkipped
	}
	changes := s.Updatable() || (s.Entry.Pinned && s.UnpinChanges())
	if !changes && !s.Entry.Pinned {
		return resultSkipped
	}
	if changes && s.Modified && !force {
		fmt.Fprintf(errOut, "warning: skipping %s: it was modified locally and updating would overwrite your changes (use --force)\n", s.Entry.Name)
		return resultSkipped
	}
	var (
		e   lock.Entry
		err error
	)
	if s.Entry.Pinned {
		var reinstalled bool
		e, reinstalled, err = update.Unpin(in, s)
		if err == nil && !reinstalled {
			fmt.Fprintf(out, "unpinned %s (already at the latest version %s)\n", e.Name, e.Version)
			return resultUnpinned
		}
	} else {
		e, err = update.Apply(in, s)
	}
	if err != nil {
		fmt.Fprintf(errOut, "%s: %v\n", s.Entry.Name, err)
		return resultFailed
	}
	if s.Modified {
		fmt.Fprintf(errOut, "warning: overwrote local changes to %s\n", e.Name)
	}
	fmt.Fprintf(out, "updated %s (%s)\n", e.Name, install.Describe(e))
	return resultUpdated
}

func newRemoveCommand() *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:     "remove <skill...>",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove installed skills",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, in, err := scope.open()
			if err != nil {
				return err
			}
			for _, name := range args {
				if err := in.Uninstall(name); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "removed", name)
			}
			return in.Lock.Save()
		},
	}
	scope.register(cmd)
	return cmd
}

func newInitCommand() *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create an empty lockfile",
		Long: `Create an empty lockfile in the project (or the global scope with --global),
so it can be committed before any skill is installed. Once created, the
lockfile is kept even when its last skill is removed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, in, err := scope.open()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if err := in.Lock.Init(); errors.Is(err, fs.ErrExist) {
				fmt.Fprintf(out, "%s already exists\n", in.Lock.Path())
				return nil
			} else if err != nil {
				return err
			}
			fmt.Fprintln(out, "created", in.Lock.Path())
			if n := len(in.Adoptable()); n > 0 {
				fmt.Fprintf(out, "%d unmanaged skill(s) found; run \"faehigkeiten adopt --all%s\" to manage them\n", n, scope.args())
			}
			return nil
		},
	}
	scope.register(cmd)
	return cmd
}

func newTrackCommand() *cobra.Command {
	var (
		scope scopeFlags
		all   bool
	)
	cmd := &cobra.Command{
		Use:   "track <version|hash|none> [skill...]",
		Short: "Change how installed skills are checked for updates",
		Long: `Change the tracking mode of installed skills. No files are reinstalled: the
recorded content hash is kept, so local edits are still detected, and the
version is the recorded one or else read from the installed copy. When the
installed copy has no version, the source is fetched: if upstream declares
one, the skill is tracked from 0.0.0, so any upstream version is offered as
an update. Skills that were untracked use their installed content as the
baseline. The next check compares against the new mode.

Pinned skills always use version tracking; unpin them first with
"faehigkeiten update <skill> --unpin". With --all, skills that cannot use the
mode are skipped and reported.`,
		Example: `  faehigkeiten track hash pdf
  faehigkeiten track version --all
  faehigkeiten track none my-notes`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 1) {
				return errors.New("name the skills to change or use --all")
			}
			mode, err := lock.ParseTracking(args[0])
			if err != nil {
				return err
			}
			a, in, err := scope.open()
			if err != nil {
				return err
			}
			names := args[1:]
			if all {
				if len(in.Lock.Skills) == 0 {
					return fmt.Errorf("no managed skills in this target (see \"faehigkeiten adopt\")")
				}
				for _, e := range in.Lock.Skills {
					names = append(names, e.Name)
				}
			}
			// Every skill is switched before anything is reported, so an
			// error leaves the lockfile unsaved and nothing claimed as done.
			r := a.NewRetracker(in)
			var lines []string
			changed := false
			for _, name := range names {
				prev, ok := in.Lock.Get(name)
				if !ok {
					return errNotInstalled(name)
				}
				if prev.Tracking == mode {
					lines = append(lines, fmt.Sprintf("%s already uses %s tracking", name, mode))
					continue
				}
				e, err := r.Retracked(cmd.Context(), prev, mode)
				switch {
				case err != nil && !all:
					return err
				case err != nil:
					fmt.Fprintf(cmd.ErrOrStderr(), "skipping %s: %v\n", name, err)
				default:
					in.Lock.Upsert(e)
					changed = true
					lines = append(lines, fmt.Sprintf("%s: %s → %s", name, install.Describe(prev), install.Describe(e)))
				}
			}
			if len(lines) == 0 {
				return fmt.Errorf("no skill can use %s tracking", mode)
			}
			if changed {
				if err := in.Lock.Save(); err != nil {
					return err
				}
			}
			for _, l := range lines {
				fmt.Fprintln(cmd.OutOrStdout(), l)
			}
			return nil
		},
	}
	scope.register(cmd)
	cmd.Flags().BoolVar(&all, "all", false, "change every installed skill")
	return cmd
}

func newSearchCommand() *cobra.Command {
	var (
		refresh bool
		remote  bool
		limit   int
	)
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search skills in known repositories (or on skills.sh with --remote)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			q := strings.Join(args, " ")
			out := cmd.OutOrStdout()
			if remote {
				hits, err := a.Remote.Search(cmd.Context(), q, limit)
				if err != nil {
					return err
				}
				tw := newTable(out, "NAME\tINSTALLS\tSOURCE")
				for _, h := range hits {
					fmt.Fprintf(tw, "%s\t%d\t%s\n", h.Name, h.Installs, h.Source)
				}
				return tw.Flush()
			}
			idx, err := a.Index(cmd.Context(), refresh, func(done, total int, r registry.Repo, _ []registry.Entry, err error) {
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "[%d/%d] %s: %v\n", done, total, r.Name, err)
				}
			})
			if err != nil {
				return err
			}
			hits := idx.Search(q)
			if len(hits) > limit {
				hits = hits[:limit]
			}
			tw := newTable(out, "NAME\tREPO\tVERSION\tLICENSE\tDESCRIPTION")
			for _, h := range hits {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", h.Name, h.Repo.Name, dash(h.Version), licenseColumn(h.License), truncate(h.Description, 60))
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "re-fetch all repositories before searching")
	cmd.Flags().BoolVar(&remote, "remote", false, "search the skills.sh directory instead of known repositories")
	cmd.Flags().IntVarP(&limit, "limit", "n", 30, "maximum number of results")
	return cmd
}

func newRepoCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "repo", Short: "Manage skill repositories"}
	var name, ref string
	add := &cobra.Command{
		Use:   "add <source>",
		Short: "Add a skill repository (owner/repo, git URL or local directory)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := source.Normalize(args[0])
			if err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if !cfg.AddRepo(config.Repo{Name: name, URL: src, Ref: ref}) {
				return fmt.Errorf("%s is already configured", src)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "added", src)
			return cfg.Save()
		},
	}
	add.Flags().StringVar(&name, "name", "", "display name")
	add.Flags().StringVar(&ref, "ref", "", "branch or tag")
	rm := &cobra.Command{
		Use:   "remove <source>",
		Short: "Remove a user-added repository",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src, err := source.Normalize(args[0])
			if err != nil {
				src = args[0]
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if !cfg.RemoveRepo(src) {
				return fmt.Errorf("%s is not a user-added repository", src)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed", src)
			return cfg.Save()
		},
	}
	list := &cobra.Command{
		Use:   "list",
		Short: "List known and user-added repositories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			tw := newTable(cmd.OutOrStdout(), "NAME\tKIND\tURL\tDESCRIPTION")
			for _, r := range registry.Repos(cfg) {
				kind := "builtin"
				if r.Custom {
					kind = "custom"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, kind, r.URL, truncate(r.Description, 50))
			}
			return tw.Flush()
		},
	}
	cmd.AddCommand(add, rm, list)
	return cmd
}

func newAgentsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List supported agents and their skill directories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			tw := newTable(cmd.OutOrStdout(), "ID\tNAME\tPROJECT DIR\tGLOBAL DIR")
			for _, ag := range a.Agents.All() {
				global := ag.GlobalDir
				if !filepath.IsAbs(global) {
					global = "~/" + global
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", ag.ID, ag.Name, ag.ProjectDir, global)
			}
			return tw.Flush()
		},
	}
}

func newAdoptCommand() *cobra.Command {
	var (
		scope     scopeFlags
		from, ref string
		skillPath string
		track     string
		local     bool
		all       bool
	)
	cmd := &cobra.Command{
		Use:   "adopt <skill...>",
		Short: "Manage skills that were not installed by faehigkeiten",
		Long: `Record skills found in agent directories (shown as "not managed" by
"faehigkeiten list") in the lockfile, so they can be tracked for updates.
The files are left untouched.

Without --from, the known repositories are searched for a skill with the same
name; a source with identical content is preferred. Use --local to manage a
skill without a source (no update tracking). Use --all to adopt every
unmanaged skill in the target; skills that cannot be adopted are skipped and
reported.`,
		Example: `  faehigkeiten adopt pdf
  faehigkeiten adopt golang-cli --from samber/cc-skills-golang --track version
  faehigkeiten adopt my-notes --local
  faehigkeiten adopt --all`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return fmt.Errorf("name the skills to adopt or use --all")
			}
			if local && (from != "" || track != "") {
				return fmt.Errorf("--local cannot be combined with --from or --track")
			}
			if all && ref != "" && from == "" {
				// The skills come from different repositories with their own refs.
				return fmt.Errorf("--ref can only be used with --all together with --from")
			}
			if skillPath != "" && (all || len(args) > 1) {
				return fmt.Errorf("--path can only be used with a single skill")
			}
			a, err := app.New()
			if err != nil {
				return err
			}
			tracking, err := parseTrack(track)
			if err != nil {
				return err
			}
			in, err := scope.installer(a)
			if err != nil {
				return err
			}
			found, err := collectFound(in, args, all)
			if err != nil {
				return err
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			var res []app.Resolution
			if local {
				for _, f := range found {
					res = append(res, app.Resolution{Req: install.AdoptRequest{Found: f}})
				}
			} else {
				// A skill reached through --all was not named by the user, so
				// only an identical copy is accepted as its source.
				opts := app.AdoptOptions{From: from, Ref: ref, Path: skillPath, Tracking: tracking, IdenticalOnly: all}
				res = a.NewResolver().ResolveAll(cmd.Context(), found, opts)
			}
			var reqs []install.AdoptRequest
			var errs []error
			for i, r := range res {
				f := found[i]
				if r.Err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", f.Name, adoptHint(r.Err, scope.args())))
					continue
				}
				if tracking == lock.TrackVersion && r.Req.Tracking == lock.TrackHash {
					fmt.Fprintf(errOut, "%s has no upstream version, using hash tracking\n", f.Name)
				}
				reqs = append(reqs, r.Req)
			}
			entries, adoptErrs, err := in.AdoptAll(reqs)
			errs = append(errs, adoptErrs...)
			if len(found) == 1 && len(errs) == 1 {
				// A single skill: its own error says it all.
				return errors.Unwrap(errs[0])
			}
			for _, err := range errs {
				fmt.Fprintf(errOut, "skipping %v\n", err)
			}
			if err != nil {
				return err
			}
			printAdopted(out, entries)
			if len(errs) > 0 {
				return fmt.Errorf("%d of %d skill(s) could not be adopted", len(errs), len(found))
			}
			return nil
		},
	}
	scope.register(cmd)
	cmd.Flags().StringVar(&from, "from", "", "source repository (owner/repo, git URL or local directory)")
	cmd.Flags().StringVar(&ref, "ref", "", "branch or tag of the source")
	cmd.Flags().StringVar(&skillPath, "path", "", "path of the skill inside the source (default: found by name)")
	cmd.Flags().StringVarP(&track, "track", "t", "", "tracking mode: version, hash or none (default: version if upstream is versioned, hash otherwise)")
	cmd.Flags().BoolVar(&local, "local", false, "manage without a source; no update tracking")
	cmd.Flags().BoolVar(&all, "all", false, "adopt every unmanaged skill in the target")
	return cmd
}

// collectFound returns the unmanaged skills to adopt: every one with all,
// else the named ones without duplicates.
func collectFound(in *install.Installer, names []string, all bool) ([]install.Found, error) {
	if all {
		found := in.Adoptable()
		if len(found) == 0 {
			return nil, fmt.Errorf("no unmanaged skills in this target")
		}
		return found, nil
	}
	var found []install.Found
	for _, name := range names {
		if slices.ContainsFunc(found, func(f install.Found) bool { return f.Name == name }) {
			continue
		}
		f, err := app.FindUnmanaged(in, name)
		if err != nil {
			return nil, err
		}
		found = append(found, f)
	}
	return found, nil
}

func printAdopted(w io.Writer, entries []lock.Entry) {
	for _, e := range entries {
		if e.Source == "" {
			fmt.Fprintf(w, "adopted %s (local only, untracked)\n", e.Name)
		} else {
			fmt.Fprintf(w, "adopted %s from %s (%s)\n", e.Name, source.DisplayName(e.Source), install.Describe(e))
		}
	}
}

// adoptHint adds the commands that resolve a NoSourceError; scope holds the
// flags selecting the target the user ran with.
func adoptHint(err error, scope string) error {
	var ns *app.NoSourceError
	if !errors.As(err, &ns) {
		return err
	}
	if len(ns.Candidates) == 0 {
		return fmt.Errorf("%w; use --from <source> or --local", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%v\n  keep your copy without a source:\n    faehigkeiten adopt %s%s --local", err, ns.Name, scope)
	b.WriteString("\n  or track a source that differs (the next update replaces your copy):")
	for _, c := range ns.Candidates {
		fmt.Fprintf(&b, "\n    faehigkeiten adopt %s%s --from %s --path %s", ns.Name, scope, shellQuote(c.Entry.Repo.URL), shellQuote(c.Entry.Path))
		if c.Entry.Repo.Ref != "" {
			fmt.Fprintf(&b, " --ref %s", shellQuote(c.Entry.Repo.Ref))
		}
	}
	return errors.New(b.String())
}

var (
	shellSafe   = regexp.MustCompile(`^[A-Za-z0-9_./:@~+=,-]+$`)
	windowsSafe = regexp.MustCompile(`^[A-Za-z0-9_./\\:@~+=,-]+$`)
)

// shellQuote quotes s for the user's shell when it contains other characters:
// single quotes for POSIX shells, double quotes (understood by cmd.exe and
// PowerShell) on Windows, where backslashes are path separators.
func shellQuote(s string) string {
	if runtime.GOOS == "windows" {
		if windowsSafe.MatchString(s) {
			return s
		}
		return `"` + s + `"`
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
