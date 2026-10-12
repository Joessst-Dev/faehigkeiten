// Package app wires configuration, sources, agents and lockfiles together for
// the CLI and the TUI.
package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// App holds the shared services.
type App struct {
	Config    *config.Config
	Agents    *agent.Registry
	Sources   *source.Manager
	Remote    *registry.RemoteSearch
	ConfigDir string
	CacheDir  string
	Home      string

	indexerOnce sync.Once
	indexer     *registry.Indexer
}

// New loads configuration and resolves state directories.
func New() (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfgDir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	cacheDir, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &App{
		Config:    cfg,
		Agents:    agent.NewRegistry(cfg),
		Sources:   source.NewManager(cacheDir),
		Remote:    registry.NewRemoteSearch(),
		ConfigDir: cfgDir,
		CacheDir:  cacheDir,
		Home:      home,
	}, nil
}

// Target builds an install target. For project scope, projectDir defaults to
// the git root of the working directory.
func (a *App) Target(scope agent.Scope, projectDir string) (install.Target, error) {
	t := install.Target{Scope: scope, Home: a.Home}
	if scope == agent.ScopeProject {
		root, err := ProjectRoot(projectDir)
		if err != nil {
			return t, err
		}
		t.ProjectRoot = root
	}
	return t, nil
}

// Installer loads the lockfile for t and returns an installer for it.
func (a *App) Installer(t install.Target) (*install.Installer, error) {
	p, err := lock.PathFor(t.Scope, t.ProjectRoot, a.ConfigDir)
	if err != nil {
		return nil, err
	}
	lf, err := lock.Load(p)
	if err != nil {
		return nil, err
	}
	return &install.Installer{Target: t, Lock: lf, Registry: a.Agents}, nil
}

// DefaultAgents returns the agents to preselect: the configured defaults,
// otherwise agents detected in the target, otherwise Claude Code.
func (a *App) DefaultAgents(t install.Target) []string {
	if len(a.Config.DefaultAgents) > 0 {
		return a.Config.DefaultAgents
	}
	if ids := a.Agents.Detect(t.Scope, t.ProjectRoot, t.Home); len(ids) > 0 {
		return ids
	}
	return []string{"claude-code"}
}

// Repos returns user and builtin repositories. It is safe to call from
// background tasks while the configuration's repositories change.
func (a *App) Repos() []registry.Repo { return registry.Repos(a.Config) }

// Indexer returns the shared background indexer.
func (a *App) Indexer() *registry.Indexer {
	a.indexerOnce.Do(func() {
		a.indexer = &registry.Indexer{
			Fetcher: a.Sources,
			Path:    registry.IndexPath(a.CacheDir),
			Repos:   a.Repos,
			TTL:     registry.DefaultTTL,
		}
	})
	return a.indexer
}

// Index returns the skill index, waiting for it to be built or updated when
// it is missing, stale, lacks configured repositories or refresh is set.
func (a *App) Index(ctx context.Context, refresh bool, progress registry.Progress) (*registry.Index, error) {
	return a.Indexer().Wait(ctx, refresh, progress)
}

// Load fetches a source and discovers its skills.
func (a *App) Load(ctx context.Context, src, ref string, refresh bool) (*source.Checkout, []skill.Skill, error) {
	co, err := a.Sources.Fetch(ctx, src, ref, refresh)
	if err != nil {
		return nil, nil, err
	}
	skills, err := skill.Discover(co.Dir)
	if err != nil {
		return nil, nil, err
	}
	return co, skills, nil
}

// Select picks skills by name or path. An empty selection is an error.
func Select(skills []skill.Skill, names []string) ([]skill.Skill, error) {
	var out []skill.Skill
	for _, n := range names {
		found := false
		for _, s := range skills {
			if strings.EqualFold(s.Name, n) || s.Path == strings.Trim(n, "/") {
				out = append(out, s)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("skill %q not found in source", n)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no skills selected")
	}
	return out, nil
}

// ProjectRoot returns the git root containing dir, or dir itself when it is
// not inside a repository. An empty dir means the working directory.
func ProjectRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	// Only "~" and "~/..." refer to the user's home; "~other" is left alone.
	if dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, dir[1:])
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	for d := abs; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return abs, nil
		}
		d = parent
	}
}

// Candidate is a possible upstream source for an unmanaged skill.
type Candidate struct {
	Entry registry.Entry
	// Identical reports whether the upstream content equals the local copy.
	Identical bool
}

// Candidates looks up skills with the same name as f in the known
// repositories. Identical copies come first, then repositories in registry
// order (user repositories, then by popularity).
func (a *App) Candidates(ctx context.Context, f install.Found) ([]Candidate, error) {
	idx, err := a.Index(ctx, false, nil)
	if err != nil {
		return nil, err
	}
	local, err := skill.Hash(f.Dirs[0])
	if err != nil {
		return nil, err
	}
	order := map[string]int{}
	for i, r := range a.Repos() {
		order[r.URL] = i
	}
	var out []Candidate
	for _, e := range idx.Entries {
		if install.DirName(e.Name) != f.Name && path.Base(e.Path) != f.Name {
			continue
		}
		if _, ok := order[e.Repo.URL]; !ok {
			// Removed while the index was built.
			continue
		}
		c := Candidate{Entry: e}
		if co, err := a.Sources.Fetch(ctx, e.Repo.URL, e.Repo.Ref, false); err == nil {
			if dir, err := co.SkillDir(e.Path); err == nil {
				if h, err := skill.Hash(dir); err == nil {
					c.Identical = h == local
				}
			}
		}
		out = append(out, c)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Identical != out[j].Identical {
			return out[i].Identical
		}
		return order[out[i].Entry.Repo.URL] < order[out[j].Entry.Repo.URL]
	})
	return out, nil
}

// AdoptOptions controls how ResolveAdopt chooses the source of a skill.
type AdoptOptions struct {
	// From, Ref and Path name the source explicitly. Without From the known
	// repositories are searched for a skill with the same name.
	From, Ref, Path string
	// Tracking is the requested mode; empty picks version tracking for
	// versioned upstreams and hash tracking otherwise.
	Tracking lock.Tracking
	// IdenticalOnly accepts a source only when its content, at the ref that
	// will be tracked, equals the local copy. Without it, an explicit source or
	// the only skill with that name is accepted even if it differs. Use it when
	// the user has not looked at each skill.
	IdenticalOnly bool
}

// NoSourceError reports that no source could be chosen automatically.
type NoSourceError struct {
	Name       string
	Candidates []Candidate
}

func (e *NoSourceError) Error() string {
	switch len(e.Candidates) {
	case 0:
		return fmt.Sprintf("no known repository contains a skill named %s", e.Name)
	case 1:
		return fmt.Sprintf("%s differs from its only source, %s", e.Name, e.Candidates[0].Entry.Repo.Name)
	}
	return fmt.Sprintf("%d repositories contain %s and none matches the local copy exactly", len(e.Candidates), e.Name)
}

// ResolveAdopt builds the request that adopts f from its upstream source. When
// version tracking is requested but upstream has no version, it falls back to
// hash tracking; callers can compare req.Tracking with opts.Tracking. An empty
// Tracking becomes install.DefaultAdoptTracking.
func (a *App) ResolveAdopt(ctx context.Context, f install.Found, opts AdoptOptions) (install.AdoptRequest, error) {
	src, ref, p := opts.From, opts.Ref, opts.Path
	var cands []Candidate
	if src == "" {
		var err error
		cands, err = a.Candidates(ctx, f)
		if err != nil {
			return install.AdoptRequest{}, err
		}
		ok := len(cands) > 0 && (cands[0].Identical || len(cands) == 1 && !opts.IdenticalOnly)
		if !ok {
			return install.AdoptRequest{}, &NoSourceError{Name: f.Name, Candidates: cands}
		}
		src, p = cands[0].Entry.Repo.URL, cands[0].Entry.Path
		if ref == "" {
			ref = cands[0].Entry.Repo.Ref
		}
	}
	co, up, err := a.Upstream(ctx, src, ref, p, f.Name)
	if err != nil {
		return install.AdoptRequest{}, err
	}
	if opts.IdenticalOnly {
		// The candidate check above used the repository's own ref and only the
		// first copy; this compares what is actually tracked with every copy,
		// since an update replaces them all, and covers explicit sources.
		h, err := skill.Hash(up.Dir)
		if err != nil {
			return install.AdoptRequest{}, err
		}
		for _, d := range f.Dirs {
			local, err := skill.Hash(d)
			if err != nil {
				return install.AdoptRequest{}, err
			}
			if local != h {
				if cands == nil {
					repo := registry.Repo{Name: source.DisplayName(co.URL), URL: co.URL, Ref: co.Ref}
					cands = []Candidate{{Entry: registry.Entry{Name: up.Name, Path: up.Path, Repo: repo}}}
				}
				return install.AdoptRequest{}, &NoSourceError{Name: f.Name, Candidates: cands}
			}
		}
	}
	req := install.AdoptRequest{Found: f, Checkout: co, Upstream: up, Tracking: opts.Tracking}
	switch {
	case req.Tracking == "":
		req.Tracking = install.DefaultAdoptTracking(*up)
	case req.Tracking == lock.TrackVersion && !up.HasVersion():
		req.Tracking = lock.TrackHash
	}
	return req, nil
}

// Upstream fetches src and returns the skill at skillPath, or the skill named
// name when skillPath is empty.
func (a *App) Upstream(ctx context.Context, src, ref, skillPath, name string) (*source.Checkout, *skill.Skill, error) {
	co, skills, err := a.Load(ctx, src, ref, false)
	if err != nil {
		return nil, nil, err
	}
	for _, s := range skills {
		if skillPath != "" && s.Path == strings.Trim(skillPath, "/") {
			return co, &s, nil
		}
	}
	if skillPath == "" {
		for _, s := range skills {
			if install.DirName(s.Name) == name || path.Base(s.Path) == name {
				return co, &s, nil
			}
		}
	}
	want := skillPath
	if want == "" {
		want = name
	}
	return nil, nil, fmt.Errorf("skill %q not found in %s", want, source.DisplayName(co.URL))
}

// FindUnmanaged returns the unmanaged skill with the given name.
func FindUnmanaged(in *install.Installer, name string) (install.Found, error) {
	for _, f := range in.Unmanaged() {
		if f.Name == name {
			return f, nil
		}
	}
	if _, ok := in.Lock.Get(name); ok {
		return install.Found{}, fmt.Errorf("%s is already managed", name)
	}
	return install.Found{}, fmt.Errorf("no unmanaged skill named %q in this target", name)
}
