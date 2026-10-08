package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"

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
	)
	cmd := &cobra.Command{
		Use:   "install <source> [skill...]",
		Short: "Install skills from a repository",
		Long: `Install skills from a source: "owner/repo" (GitHub), a git URL or a local directory.
Select skills by name or path, or use --all.

Tracking modes:
  version  compare the version in SKILL.md (default for versioned skills)
  hash     compare a content hash of the skill directory
  none     never check for updates (default for unversioned skills)`,
		Example: `  faehigkeiten install anthropics/skills pdf docx
  faehigkeiten install anthropics/skills --list
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
			selected := skills
			if !all {
				if len(args) == 1 {
					return fmt.Errorf("name the skills to install or pass --all (use --list to see %d available skills)", len(skills))
				}
				if selected, err = app.Select(skills, args[1:]); err != nil {
					return err
				}
			}
			var tracking lock.Tracking
			if track != "" {
				if tracking, err = lock.ParseTracking(track); err != nil {
					return err
				}
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
			for _, s := range selected {
				tr := tracking
				if tr == lock.TrackVersion && !s.HasVersion() {
					tr = lock.TrackHash
					fmt.Fprintf(cmd.ErrOrStderr(), "%s has no version, using hash tracking\n", s.Name)
				}
				e, err := in.Install(install.Request{Skill: s, Checkout: co, Agents: targets, Tracking: tr, Force: force})
				if err != nil {
					return err
				}
				fmt.Fprintf(out, "installed %s (%s) for %s\n", e.Name, describeTracking(e), strings.Join(e.Agents, ", "))
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
	return cmd
}

func describeTracking(e lock.Entry) string {
	switch e.Tracking {
	case lock.TrackVersion:
		return "version " + e.Version
	case lock.TrackHash:
		return "hash " + shortHash(e.Hash)
	}
	return "untracked"
}

func shortHash(h string) string {
	h = strings.TrimPrefix(h, skill.HashPrefix)
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func printSkills(w io.Writer, skills []skill.Skill) {
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tVERSION\tPATH\tDESCRIPTION")
	for _, s := range skills {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, dash(s.Version), s.Path, truncate(s.Description, 70))
	}
	tw.Flush()
}

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
			a, err := app.New()
			if err != nil {
				return err
			}
			t, err := scope.target(a)
			if err != nil {
				return err
			}
			in, err := a.Installer(t)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			found := in.Unmanaged()
			if len(in.Lock.Skills) == 0 && len(found) == 0 {
				fmt.Fprintln(out, "no skills installed")
				return nil
			}
			tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTRACKING\tAGENTS\tSOURCE")
			for _, e := range in.Lock.Skills {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Name, describeTracking(e), strings.Join(e.Agents, ","), source.DisplayName(e.Source))
			}
			for _, f := range found {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", f.Name, "not managed", strings.Join(f.Agents, ","), "-")
			}
			return tw.Flush()
		},
	}
	scope.register(cmd)
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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			t, err := scope.target(a)
			if err != nil {
				return err
			}
			in, err := a.Installer(t)
			if err != nil {
				return err
			}
			sts := update.Check(cmd.Context(), a.Sources, in.Lock)
			n := printStatuses(cmd.OutOrStdout(), sts)
			if n > 0 && exitCode {
				return ExitError{Code: 10}
			}
			return nil
		},
	}
	scope.register(cmd)
	cmd.Flags().BoolVar(&exitCode, "exit-code", false, "exit with status 10 when updates are available")
	return cmd
}

func printStatuses(w io.Writer, sts []update.Status) int {
	if len(sts) == 0 {
		fmt.Fprintln(w, "no tracked skills")
		return 0
	}
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tTRACKING\tINSTALLED\tLATEST\tSTATUS")
	n := 0
	for _, s := range sts {
		status := "up to date"
		switch {
		case s.Err != nil:
			status = "error: " + s.Err.Error()
		case s.Available:
			status = "update available"
			n++
		}
		cur, latest := s.Current, s.Latest
		if s.Entry.Tracking == lock.TrackHash {
			cur, latest = shortHash(cur), shortHash(latest)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", s.Entry.Name, s.Entry.Tracking, dash(cur), dash(latest), status)
	}
	tw.Flush()
	return n
}

func newUpdateCommand() *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:   "update [skill...]",
		Short: "Update tracked skills (all with available updates by default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			t, err := scope.target(a)
			if err != nil {
				return err
			}
			in, err := a.Installer(t)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			updated := 0
			for _, s := range update.Check(cmd.Context(), a.Sources, in.Lock) {
				if len(args) > 0 && !contains(args, s.Entry.Name) {
					continue
				}
				if s.Err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", s.Entry.Name, s.Err)
					continue
				}
				if !s.Available {
					continue
				}
				e, err := update.Apply(in, s)
				if err != nil {
					return err
				}
				updated++
				fmt.Fprintf(out, "updated %s (%s)\n", e.Name, describeTracking(e))
			}
			if updated == 0 {
				fmt.Fprintln(out, "everything is up to date")
				return nil
			}
			return in.Lock.Save()
		},
	}
	scope.register(cmd)
	return cmd
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func newRemoveCommand() *cobra.Command {
	var scope scopeFlags
	cmd := &cobra.Command{
		Use:     "remove <skill...>",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove installed skills",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := app.New()
			if err != nil {
				return err
			}
			t, err := scope.target(a)
			if err != nil {
				return err
			}
			in, err := a.Installer(t)
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
			tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
			if remote {
				hits, err := a.Remote.Search(cmd.Context(), q, limit)
				if err != nil {
					return err
				}
				fmt.Fprintln(tw, "NAME\tINSTALLS\tSOURCE")
				for _, h := range hits {
					fmt.Fprintf(tw, "%s\t%d\t%s\n", h.Name, h.Installs, h.Source)
				}
				return tw.Flush()
			}
			idx, err := a.Index(cmd.Context(), refresh, func(done, total int, r registry.Repo, err error) {
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
			fmt.Fprintln(tw, "NAME\tREPO\tVERSION\tDESCRIPTION")
			for _, h := range hits {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", h.Name, h.Repo.Name, dash(h.Version), truncate(h.Description, 60))
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
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tKIND\tURL\tDESCRIPTION")
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
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tNAME\tPROJECT DIR\tGLOBAL DIR")
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
