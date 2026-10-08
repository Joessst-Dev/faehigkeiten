// Package registry knows the well-known skill repositories, merges them with
// user-added ones and builds a searchable index of their skills.
package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

//go:embed registry.yaml
var builtinYAML []byte

// Repo is a skill repository.
type Repo struct {
	Name        string `yaml:"name" json:"name"`
	URL         string `yaml:"url" json:"url"`
	Ref         string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
	Stars       int    `yaml:"stars,omitempty" json:"stars,omitempty"`
	// Custom marks repositories added by the user.
	Custom bool `yaml:"-" json:"custom,omitempty"`
}

// Builtin returns the embedded list of well-known repositories, most popular first.
func Builtin() []Repo {
	var doc struct {
		Repos []Repo `yaml:"repos"`
	}
	if err := yaml.Unmarshal(builtinYAML, &doc); err != nil {
		panic("registry: invalid embedded registry.yaml: " + err.Error())
	}
	sort.SliceStable(doc.Repos, func(i, j int) bool { return doc.Repos[i].Stars > doc.Repos[j].Stars })
	return doc.Repos
}

// Repos returns the user's repositories followed by the builtin ones,
// deduplicated by URL.
func Repos(cfg *config.Config) []Repo {
	var out []Repo
	seen := map[string]bool{}
	if cfg != nil {
		for _, r := range cfg.Repos {
			name := r.Name
			if name == "" {
				name = source.DisplayName(r.URL)
			}
			out = append(out, Repo{Name: name, URL: r.URL, Ref: r.Ref, Custom: true})
			seen[r.URL] = true
		}
	}
	for _, r := range Builtin() {
		if !seen[r.URL] {
			out = append(out, r)
		}
	}
	return out
}

// Entry is a skill in the index.
type Entry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	Path        string `json:"path"`
	Repo        Repo   `json:"repo"`
}

// Index is a searchable list of skills across repositories.
type Index struct {
	BuiltAt time.Time         `json:"built_at"`
	Entries []Entry           `json:"entries"`
	Errors  map[string]string `json:"errors,omitempty"`
}

// Fetcher makes a source available on disk.
type Fetcher interface {
	Fetch(ctx context.Context, src, ref string, refresh bool) (*source.Checkout, error)
}

// Progress is called after each repository was indexed.
type Progress func(done, total int, repo Repo, err error)

// Build indexes all repos concurrently. Failing repos are recorded in
// Index.Errors and do not abort the build.
func Build(ctx context.Context, f Fetcher, repos []Repo, refresh bool, progress Progress) *Index {
	idx := &Index{BuiltAt: time.Now().UTC(), Errors: map[string]string{}}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		done int
		sem  = make(chan struct{}, 4)
	)
	for _, r := range repos {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			entries, err := Scan(ctx, f, r, refresh)
			mu.Lock()
			defer mu.Unlock()
			done++
			if err != nil {
				idx.Errors[r.URL] = err.Error()
			} else {
				idx.Entries = append(idx.Entries, entries...)
			}
			if progress != nil {
				progress(done, len(repos), r, err)
			}
		}()
	}
	wg.Wait()
	idx.sort()
	return idx
}

// Scan fetches a single repository and lists its skills.
func Scan(ctx context.Context, f Fetcher, r Repo, refresh bool) ([]Entry, error) {
	co, err := f.Fetch(ctx, r.URL, r.Ref, refresh)
	if err != nil {
		return nil, err
	}
	skills, err := skill.Discover(co.Dir)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(skills))
	for _, s := range skills {
		entries = append(entries, Entry{Name: s.Name, Description: s.Description, Version: s.Version, Path: s.Path, Repo: r})
	}
	return entries, nil
}

func (idx *Index) sort() {
	slices.SortStableFunc(idx.Entries, func(a, b Entry) int {
		if c := strings.Compare(a.Repo.Name, b.Repo.Name); c != 0 {
			return c
		}
		return strings.Compare(a.Path, b.Path)
	})
}

// Search returns entries matching all whitespace-separated terms in name,
// description or repository. Name matches rank first.
func (idx *Index) Search(query string) []Entry {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return slices.Clone(idx.Entries)
	}
	type scored struct {
		e     Entry
		score int
	}
	var hits []scored
	for _, e := range idx.Entries {
		name := strings.ToLower(e.Name)
		hay := name + "\n" + strings.ToLower(e.Description) + "\n" + strings.ToLower(e.Repo.Name)
		score, ok := 0, true
		for _, t := range terms {
			switch {
			case name == t:
				score += 100
			case strings.HasPrefix(name, t):
				score += 50
			case strings.Contains(name, t):
				score += 20
			case strings.Contains(hay, t):
				score++
			default:
				ok = false
			}
		}
		if ok {
			hits = append(hits, scored{e, score})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	out := make([]Entry, len(hits))
	for i, h := range hits {
		out[i] = h.e
	}
	return out
}

// DefaultTTL is how long a cached index is considered fresh.
const DefaultTTL = 24 * time.Hour

// IndexPath returns where the index cache is stored.
func IndexPath(cacheDir string) string { return filepath.Join(cacheDir, "index.json") }

// LoadIndex reads a cached index. It returns (nil, nil) when no cache exists
// or it is older than ttl.
func LoadIndex(p string, ttl time.Duration) (*Index, error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, nil // corrupt cache: rebuild
	}
	if ttl > 0 && time.Since(idx.BuiltAt) > ttl {
		return nil, nil
	}
	return &idx, nil
}

// Save writes the index cache.
func (idx *Index) Save(p string) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
