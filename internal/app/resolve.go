package app

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"

	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// adoptConcurrency bounds how many skills ResolveAll resolves at once.
const adoptConcurrency = 8

// Resolver chooses adopt sources. It reads the index once, loads and scans
// each repository once and hashes each directory once, so resolving many
// skills from the same repository does not repeat that work. Failures are
// shared by concurrent callers but not remembered, so a later call retries.
// It is safe for concurrent use. It assumes sources and local copies do not
// change while it is used, so use one per run or per screen.
type Resolver struct {
	app *App

	idx    memo[indexed]
	loads  memo[loaded]
	hashes memo[string]
}

type indexed struct {
	idx   *registry.Index
	order map[string]int // registry position by URL
}

type loaded struct {
	co     *source.Checkout
	skills []skill.Skill
}

// NewResolver returns a Resolver for one adopt run.
func (a *App) NewResolver() *Resolver {
	return &Resolver{app: a}
}

// memo computes a value once per key, even when asked concurrently. A failed
// computation is forgotten once its callers have its error, so it is retried.
type memo[V any] struct {
	mu sync.Mutex
	m  map[string]*memoEntry[V]
}

type memoEntry[V any] struct {
	once sync.Once
	v    V
	err  error
}

func (c *memo[V]) get(key string, fn func() (V, error)) (V, error) {
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*memoEntry[V]{}
	}
	e, ok := c.m[key]
	if !ok {
		e = &memoEntry[V]{}
		c.m[key] = e
	}
	c.mu.Unlock()
	e.once.Do(func() { e.v, e.err = fn() })
	if e.err != nil {
		c.mu.Lock()
		if c.m[key] == e {
			delete(c.m, key)
		}
		c.mu.Unlock()
	}
	return e.v, e.err
}

func (c *memo[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.m)
}

func (r *Resolver) index(ctx context.Context) (*registry.Index, map[string]int, error) {
	x, err := r.idx.get("", func() (indexed, error) {
		idx, err := r.app.Index(ctx, false, nil)
		if err != nil {
			return indexed{}, err
		}
		// Drop repositories removed while the index was built.
		repos := r.app.Repos()
		order := map[string]int{}
		for i, repo := range repos {
			order[repo.URL] = i
		}
		return indexed{idx.Retain(repos), order}, nil
	})
	return x.idx, x.order, err
}

func (r *Resolver) load(ctx context.Context, src, ref string) (*source.Checkout, []skill.Skill, error) {
	// NUL cannot appear in a URL or ref, so keys cannot collide.
	l, err := r.loads.get(src+"\x00"+ref, func() (loaded, error) {
		co, skills, err := r.app.Load(ctx, src, ref, false)
		return loaded{co, skills}, err
	})
	return l.co, l.skills, err
}

func (r *Resolver) hash(dir string) (string, error) {
	return r.hashes.get(dir, func() (string, error) { return skill.Hash(dir) })
}

// Candidates looks up skills with the same name as f in the known
// repositories. Identical copies come first, then repositories in registry
// order (user repositories, then by popularity).
func (r *Resolver) Candidates(ctx context.Context, f install.Found) ([]Candidate, error) {
	idx, order, err := r.index(ctx)
	if err != nil {
		return nil, err
	}
	local, err := r.hash(f.Dirs[0])
	if err != nil {
		return nil, err
	}
	var out []Candidate
	for _, e := range idx.Entries {
		if install.DirName(e.Name) != f.Name && path.Base(e.Path) != f.Name {
			continue
		}
		c := Candidate{Entry: e}
		if _, skills, err := r.load(ctx, e.Repo.URL, e.Repo.Ref); err == nil {
			for _, s := range skills {
				if s.Path != e.Path {
					continue
				}
				if h, err := r.hash(s.Dir); err == nil {
					c.Identical = h == local
				}
				break
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

// ResolveAdopt builds the request that adopts f from its upstream source. When
// version tracking is requested but upstream has no version, it falls back to
// hash tracking; callers can compare req.Tracking with opts.Tracking. An empty
// Tracking becomes install.DefaultAdoptTracking.
func (r *Resolver) ResolveAdopt(ctx context.Context, f install.Found, opts AdoptOptions) (install.AdoptRequest, error) {
	if len(f.Dirs) == 0 {
		return install.AdoptRequest{}, fmt.Errorf("skill %s has no directories", f.Name)
	}
	src, ref, p := opts.From, opts.Ref, opts.Path
	var cands []Candidate
	if src == "" {
		var err error
		cands, err = r.Candidates(ctx, f)
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
	co, up, err := r.Upstream(ctx, src, ref, p, f.Name)
	if err != nil {
		return install.AdoptRequest{}, err
	}
	if opts.IdenticalOnly {
		// The candidate check above used the repository's own ref and only the
		// first copy; this compares what is actually tracked with every copy,
		// since an update replaces them all, and covers explicit sources.
		upHash, err := r.hash(up.Dir)
		if err != nil {
			return install.AdoptRequest{}, err
		}
		for _, d := range f.Dirs {
			local, err := r.hash(d)
			if err != nil {
				return install.AdoptRequest{}, err
			}
			if local != upHash {
				if cands == nil {
					repo := registry.Repo{Name: source.DisplayName(co.URL), URL: co.URL, Ref: co.Ref}
					cands = []Candidate{{Entry: registry.Entry{Name: up.Name, Path: up.Path, Repo: repo}}}
				}
				return install.AdoptRequest{}, &NoSourceError{Name: f.Name, Candidates: cands}
			}
		}
	}
	// Adopt hashes the copies again when it records them: the request may
	// wait for confirmation while the files change.
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
func (r *Resolver) Upstream(ctx context.Context, src, ref, skillPath, name string) (*source.Checkout, *skill.Skill, error) {
	co, skills, err := r.load(ctx, src, ref)
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

// Resolution is the outcome of resolving one skill.
type Resolution struct {
	Req install.AdoptRequest
	Err error
}

// ResolveAll resolves found in parallel, each with opts, and returns the
// results in the same order. Skills not started before ctx is cancelled fail
// with its error.
func (r *Resolver) ResolveAll(ctx context.Context, found []install.Found, opts AdoptOptions) []Resolution {
	out := make([]Resolution, len(found))
	var wg sync.WaitGroup
	sem := make(chan struct{}, adoptConcurrency)
	for i, f := range found {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				out[i].Err = err
				return
			}
			out[i].Req, out[i].Err = r.ResolveAdopt(ctx, f, opts)
		})
	}
	wg.Wait()
	return out
}
