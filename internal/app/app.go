// Package app wires configuration, sources, agents and lockfiles together for
// the CLI and the TUI.
package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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
	if ids := a.Config.DefaultAgentList(); len(ids) > 0 {
		return ids
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

// Versions lists the versions of the skill at path in the branch or tag ref
// of src, newest first. Refresh updates the cached history first.
func (a *App) Versions(ctx context.Context, src, ref, path string, refresh bool) ([]source.Version, error) {
	vs, err := a.Sources.Versions(ctx, src, ref, path, refresh)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, fmt.Errorf("skill at %s has no versions in its history", path)
	}
	return vs, nil
}

// LoadVersion fetches the skill at path of src as it was at version.
// Refresh updates the cached history first; installing several versions from
// one source only needs to do that once.
func (a *App) LoadVersion(ctx context.Context, src, ref, path, version string, refresh bool) (*source.Checkout, skill.Skill, error) {
	vs, err := a.Versions(ctx, src, ref, path, refresh)
	if err != nil {
		return nil, skill.Skill{}, err
	}
	i := slices.IndexFunc(vs, func(v source.Version) bool { return v.Version == version })
	if i < 0 {
		known := make([]string, len(vs))
		for i, v := range vs {
			known[i] = v.Version
		}
		return nil, skill.Skill{}, fmt.Errorf("version %s of %s not found (available: %s)", version, path, strings.Join(known, ", "))
	}
	return a.LoadCommit(ctx, src, ref, path, vs[i].Commit)
}

// LoadCommit fetches the skill at path of src as it was at commit.
func (a *App) LoadCommit(ctx context.Context, src, ref, path, commit string) (*source.Checkout, skill.Skill, error) {
	co, err := a.Sources.FetchCommit(ctx, src, ref, commit, path)
	if err != nil {
		return nil, skill.Skill{}, err
	}
	dir, err := co.SkillDir(path)
	if err != nil {
		return nil, skill.Skill{}, err
	}
	s, err := skill.LoadDir(dir, co.Dir)
	if err != nil {
		return nil, skill.Skill{}, fmt.Errorf("skill %s at %s: %w", path, commit, err)
	}
	s.Path = path
	return co, s, nil
}

// KeptPin returns the lockfile entry that keeps s pinned when it is installed
// from co without a version: a pin of the same source, path and ref.
// Installing from another ref replaces the pin.
func KeptPin(in *install.Installer, co *source.Checkout, s skill.Skill) (lock.Entry, bool) {
	e, ok := in.Lock.Get(install.DirName(s.Name))
	if !ok || !e.Pinned || e.Commit == "" || e.Source != co.URL || e.Path != s.Path || e.Ref != co.Ref {
		return lock.Entry{}, false
	}
	return e, true
}

// Selector chooses the version of a skill to install.
type Selector struct {
	// Version is "" to keep an existing pin, Latest to install the checkout
	// and remove a pin, or a version to install pinned.
	Version string
	// Commit optionally names the commit of Version when it is already known,
	// which saves walking the history again.
	Commit string
}

// Pin describes whether installing a skill pins it, and at which version and
// commit ("" when the commit still has to be looked up).
type Pin struct {
	Pinned          bool
	Version, Commit string
}

// PinFor decides, without fetching anything, whether installing s from co
// with sel pins it:
//   - "": only if the skill is pinned already (see KeptPin), which keeps it
//     at its pinned commit;
//   - Latest: never; a pin is removed;
//   - anything else: always, at that version.
func PinFor(in *install.Installer, co *source.Checkout, s skill.Skill, sel Selector) Pin {
	switch sel.Version {
	case "":
		if e, ok := KeptPin(in, co, s); ok {
			return Pin{Pinned: true, Version: e.Version, Commit: e.Commit}
		}
	case Latest:
	default:
		return Pin{Pinned: true, Version: sel.Version, Commit: sel.Commit}
	}
	return Pin{}
}

// Prepare builds the request for installing s from co with sel, as PinFor
// decides: unpinned skills are installed from co, pinned ones from the history
// of co's ref. Refresh updates the cached history before looking up a version;
// installing several versions from one source only needs to do that once. The
// returned request has Skill, Checkout and Pinned set.
func (a *App) Prepare(ctx context.Context, in *install.Installer, co *source.Checkout, s skill.Skill, sel Selector, refresh bool) (install.Request, error) {
	req := install.Request{Skill: s, Checkout: co}
	pin := PinFor(in, co, s, sel)
	if !pin.Pinned {
		return req, nil
	}
	var err error
	if pin.Commit != "" {
		req.Checkout, req.Skill, err = a.LoadCommit(ctx, co.URL, co.Ref, s.Path, pin.Commit)
	} else {
		req.Checkout, req.Skill, err = a.LoadVersion(ctx, co.URL, co.Ref, s.Path, pin.Version, refresh)
	}
	if err != nil {
		return req, fmt.Errorf("%s at %s: %w", s.Name, pin.Version, err)
	}
	req.Pinned = true
	return req, nil
}

// Latest is the version selector for the newest, unpinned skill.
const Latest = "latest"

// SplitVersion splits a "name@version" selector. Names without a version
// return an empty version. Versions cannot contain "/", so paths with an "@"
// in a directory name (e.g. "skills/@org/tool") are not split.
func SplitVersion(sel string) (name, version string) {
	if i := strings.LastIndex(sel, "@"); i > 0 && i < len(sel)-1 && !strings.Contains(sel[i+1:], "/") {
		return sel[:i], sel[i+1:]
	}
	return sel, ""
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
