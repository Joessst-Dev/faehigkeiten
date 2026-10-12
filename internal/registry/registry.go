// Package registry knows the well-known skill repositories, merges them with
// user-added ones and builds a searchable index of their skills.
package registry

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
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
func Builtin() []Repo { return slices.Clone(builtin()) }

// builtin parses the embedded list once. The result must not be modified.
var builtin = sync.OnceValue(func() []Repo {
	var doc struct {
		Repos []Repo `yaml:"repos"`
	}
	if err := yaml.Unmarshal(builtinYAML, &doc); err != nil {
		panic("registry: invalid embedded registry.yaml: " + err.Error())
	}
	sort.SliceStable(doc.Repos, func(i, j int) bool { return doc.Repos[i].Stars > doc.Repos[j].Stars })
	return doc.Repos
})

// Repos returns the user's repositories followed by the builtin ones,
// deduplicated by URL.
func Repos(cfg *config.Config) []Repo {
	var out []Repo
	seen := map[string]bool{}
	if cfg != nil {
		for _, r := range cfg.RepoList() {
			name := r.Name
			if name == "" {
				name = source.DisplayName(r.URL)
			}
			out = append(out, Repo{Name: name, URL: r.URL, Ref: r.Ref, Custom: true})
			seen[r.URL] = true
		}
	}
	for _, r := range builtin() {
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
	// Scanned records when each repository was last indexed, successfully
	// or not, so new repositories can be indexed without a full rebuild. It
	// is keyed by Repo.key, so changing a repository's ref re-indexes it.
	Scanned map[string]time.Time `json:"scanned,omitempty"`

	// legacy is set on caches written before Scanned existed until
	// finishLegacy has run.
	legacy bool
}

// key identifies what was indexed for a repository: its URL and ref.
func (r Repo) key() string {
	if r.Ref == "" {
		return r.URL
	}
	return r.URL + "#" + r.Ref
}

// WithScanned returns a copy of idx that records repos as indexed at BuiltAt
// unless they already are. idx is not modified.
func (idx *Index) WithScanned(repos ...Repo) *Index {
	out := idx.Clone()
	for _, r := range repos {
		if _, ok := out.Scanned[r.key()]; !ok {
			out.Scanned[r.key()] = out.BuiltAt
		}
	}
	return out
}

// finishLegacy completes the conversion of a cache written before Scanned
// existed. LoadIndexAny marked the repositories with entries; failures were
// recorded by URL only, so each configured repository that failed is marked
// under its current ref, to be retried after ErrorRetry like any failure.
// Repositories with neither are left unmarked and indexed once: they may have
// been added after the cache was built. So are repositories whose entries came
// from another ref.
func (idx *Index) finishLegacy(repos []Repo) {
	indexed := map[string]bool{}
	for _, e := range idx.Entries {
		indexed[e.Repo.URL] = true
	}
	for _, r := range repos {
		if _, failed := idx.Errors[r.URL]; !failed || indexed[r.URL] {
			continue
		}
		if _, ok := idx.Scanned[r.key()]; !ok {
			idx.Scanned[r.key()] = idx.BuiltAt
		}
	}
	idx.legacy = false
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{BuiltAt: time.Now().UTC(), Errors: map[string]string{}, Scanned: map[string]time.Time{}}
}

// Fetcher makes a source available on disk.
type Fetcher interface {
	Fetch(ctx context.Context, src, ref string, refresh bool) (*source.Checkout, error)
}

// Progress is called after each repository was indexed with the entries
// found in it.
type Progress func(done, total int, repo Repo, entries []Entry, err error)

// buildConcurrency limits parallel fetches. Fetching is network-bound.
const buildConcurrency = 8

// Build indexes all repos concurrently. Failing repos are recorded in
// Index.Errors and do not abort the build. Progress calls are serialized.
func Build(ctx context.Context, f Fetcher, repos []Repo, refresh bool, progress Progress) *Index {
	idx := NewIndex()
	done := 0
	scanAll(ctx, f, repos, refresh, func(r Repo, entries []Entry, err error) {
		done++
		idx.merge(r, entries, err)
		if progress != nil {
			progress(done, len(repos), r, entries, err)
		}
	})
	idx.sort()
	return idx
}

// scanAll scans repos with limited concurrency and calls fn for each result.
// Calls to fn are serialized.
func scanAll(ctx context.Context, f Fetcher, repos []Repo, refresh bool, fn func(Repo, []Entry, error)) {
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, buildConcurrency)
	)
	for _, r := range repos {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			entries, err := Scan(ctx, f, r, refresh)
			mu.Lock()
			defer mu.Unlock()
			fn(r, entries, err)
		})
	}
	wg.Wait()
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

// Merge returns a copy of idx with the result of indexing r. On success the
// repository's entries are replaced; on failure its previous entries are kept
// and the error is recorded.
func (idx *Index) Merge(r Repo, entries []Entry, err error) *Index {
	out := idx.Clone()
	out.merge(r, entries, err)
	out.sort()
	return out
}

func (idx *Index) merge(r Repo, entries []Entry, err error) {
	idx.Scanned[r.key()] = time.Now().UTC()
	if err != nil {
		idx.Errors[r.URL] = err.Error()
		return
	}
	delete(idx.Errors, r.URL)
	idx.Entries = slices.DeleteFunc(idx.Entries, func(e Entry) bool { return e.Repo.URL == r.URL })
	idx.Entries = append(idx.Entries, entries...)
}

// Clone returns a deep copy of idx.
func (idx *Index) Clone() *Index {
	out := &Index{BuiltAt: idx.BuiltAt, Entries: slices.Clone(idx.Entries), Errors: maps.Clone(idx.Errors), Scanned: maps.Clone(idx.Scanned)}
	if out.Errors == nil {
		out.Errors = map[string]string{}
	}
	if out.Scanned == nil {
		out.Scanned = map[string]time.Time{}
	}
	return out
}

// Retain returns a copy of idx without entries and errors of repositories
// that are no longer in repos, or idx itself when nothing is dropped.
func (idx *Index) Retain(repos []Repo) *Index {
	keep, keepKey := map[string]bool{}, map[string]bool{}
	for _, r := range repos {
		keep[r.URL], keepKey[r.key()] = true, true
	}
	if !idx.hasOther(keep, keepKey) {
		return idx // nothing to drop: avoid copying
	}
	out := idx.Clone()
	out.Entries = slices.DeleteFunc(out.Entries, func(e Entry) bool { return !keep[e.Repo.URL] })
	maps.DeleteFunc(out.Errors, func(u, _ string) bool { return !keep[u] })
	maps.DeleteFunc(out.Scanned, func(k string, _ time.Time) bool { return !keepKey[k] })
	return out
}

// hasOther reports whether idx holds anything outside keep (by URL) and
// keepKey (by Repo.key).
func (idx *Index) hasOther(keep, keepKey map[string]bool) bool {
	for _, e := range idx.Entries {
		if !keep[e.Repo.URL] {
			return true
		}
	}
	for u := range idx.Errors {
		if !keep[u] {
			return true
		}
	}
	for k := range idx.Scanned {
		if !keepKey[k] {
			return true
		}
	}
	return false
}

// ErrorRetry is how long a repository that failed to index is skipped
// before Missing reports it again.
const ErrorRetry = time.Hour

// Missing returns the repos that were never indexed, or that failed longer
// than ErrorRetry ago.
func (idx *Index) Missing(repos []Repo) []Repo {
	var out []Repo
	for _, r := range repos {
		at, ok := idx.Scanned[r.key()]
		_, failed := idx.Errors[r.URL]
		if !ok || failed && time.Since(at) > ErrorRetry {
			out = append(out, r)
		}
	}
	return out
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

// LoadIndexAny reads a cached index regardless of its age and reports whether
// it is younger than ttl. It returns a nil index when no usable cache exists.
func LoadIndexAny(p string, ttl time.Duration) (idx *Index, fresh bool, err error) {
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	idx = &Index{}
	if err := json.Unmarshal(data, idx); err != nil {
		return nil, false, nil // corrupt cache: rebuild
	}
	if idx.Errors == nil {
		idx.Errors = map[string]string{}
	}
	if idx.Scanned == nil {
		// Caches written before Scanned existed: repositories with entries
		// were indexed at build time. Errors are keyed by URL without the
		// ref, so the Indexer marks failed repositories once it knows the
		// configured refs; see finishLegacy.
		idx.Scanned = map[string]time.Time{}
		idx.legacy = true
		for _, e := range idx.Entries {
			idx.Scanned[e.Repo.key()] = idx.BuiltAt
		}
	}
	return idx, ttl <= 0 || time.Since(idx.BuiltAt) <= ttl, nil
}

// Save writes the index cache atomically.
func (idx *Index) Save(p string) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".index-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}
