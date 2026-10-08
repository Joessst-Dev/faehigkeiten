// Package source fetches skill repositories into a local cache.
package source

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

// Checkout is a skill repository available on disk.
type Checkout struct {
	// URL is the normalized source location.
	URL string
	// Ref is the branch or tag that was checked out ("" for plain directories).
	Ref string
	// Commit is the checked out commit hash ("" for plain directories).
	Commit string
	// Dir is the root of the working tree.
	Dir string
}

var shorthand = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Normalize turns user input into a source location: "owner/repo" and
// "github.com/owner/repo" become GitHub HTTPS URLs, existing local
// directories become absolute paths, other URLs are kept as-is.
func Normalize(input string) (string, error) {
	in := strings.TrimSpace(input)
	if in == "" {
		return "", errors.New("empty source")
	}
	if strings.HasPrefix(in, "file://") {
		return in, nil
	}
	if fi, err := os.Stat(in); err == nil && fi.IsDir() {
		return filepath.Abs(in)
	}
	if strings.HasPrefix(in, "git@") || strings.Contains(in, "://") {
		return strings.TrimSuffix(in, "/"), nil
	}
	if strings.HasPrefix(in, "github.com/") {
		return "https://" + strings.TrimSuffix(in, ".git"), nil
	}
	if shorthand.MatchString(in) {
		return "https://github.com/" + strings.TrimSuffix(in, ".git"), nil
	}
	return "", fmt.Errorf("unrecognized source %q (use owner/repo, a git URL or a local directory)", input)
}

// IsLocal reports whether a normalized source is a directory on disk.
func IsLocal(src string) bool {
	return filepath.IsAbs(src) && !strings.Contains(src, "://")
}

// DisplayName returns a short human-readable name like "owner/repo".
func DisplayName(src string) string {
	if IsLocal(src) {
		return filepath.Base(src)
	}
	s := strings.TrimSuffix(src, ".git")
	if strings.HasPrefix(s, "git@") {
		if i := strings.Index(s, ":"); i >= 0 {
			return s[i+1:]
		}
	}
	if u, err := url.Parse(s); err == nil && u.Path != "" {
		return strings.Trim(u.Path, "/")
	}
	return s
}

// Manager clones and refreshes repositories below a cache directory.
type Manager struct {
	CacheDir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewManager returns a Manager storing clones below cacheDir/repos.
func NewManager(cacheDir string) *Manager {
	return &Manager{CacheDir: cacheDir, locks: map[string]*sync.Mutex{}}
}

func (m *Manager) lock(key string) func() {
	m.mu.Lock()
	l, ok := m.locks[key]
	if !ok {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	m.mu.Unlock()
	l.Lock()
	return l.Unlock
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func (m *Manager) cachePath(src, ref string) string {
	key := strings.TrimPrefix(strings.TrimPrefix(src, "https://"), "http://")
	key = unsafeChars.ReplaceAllString(key, "_")
	if ref != "" {
		key += "@" + unsafeChars.ReplaceAllString(ref, "_")
	}
	return filepath.Join(m.CacheDir, "repos", key)
}

// Fetch makes src available on disk. Plain local directories are used in
// place. Git sources are shallow-cloned into the cache; an existing clone is
// reused unless refresh is set, in which case it is replaced by a fresh clone.
func (m *Manager) Fetch(ctx context.Context, src, ref string, refresh bool) (*Checkout, error) {
	src, err := Normalize(src)
	if err != nil {
		return nil, err
	}
	if IsLocal(src) {
		return localCheckout(src)
	}
	dir := m.cachePath(src, ref)
	defer m.lock(dir)()

	if !refresh {
		if co, err := openCheckout(src, dir); err == nil {
			return co, nil
		}
	}
	tmp := dir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if err := clone(ctx, src, ref, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return nil, err
	}
	return openCheckout(src, dir)
}

func clone(ctx context.Context, src, ref, dir string) error {
	opts := &git.CloneOptions{URL: src, Depth: 1, SingleBranch: true, Tags: git.NoTags}
	if ref == "" {
		_, err := git.PlainCloneContext(ctx, dir, false, opts)
		return wrapCloneErr(src, err)
	}
	var errs []error
	for _, name := range []plumbing.ReferenceName{plumbing.NewBranchReferenceName(ref), plumbing.NewTagReferenceName(ref)} {
		_ = os.RemoveAll(dir)
		o := *opts
		o.ReferenceName = name
		_, err := git.PlainCloneContext(ctx, dir, false, &o)
		if err == nil {
			return nil
		}
		errs = append(errs, err)
	}
	return wrapCloneErr(src, fmt.Errorf("ref %q not found: %w", ref, errors.Join(errs...)))
}

func wrapCloneErr(src string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("clone %s: %w", src, err)
}

func openCheckout(src, dir string) (*Checkout, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	return &Checkout{URL: src, Ref: head.Name().Short(), Commit: head.Hash().String(), Dir: dir}, nil
}

// localCheckout uses a local directory in place, recording git metadata when
// the directory is a repository.
func localCheckout(dir string) (*Checkout, error) {
	co := &Checkout{URL: dir, Dir: dir}
	repo, err := git.PlainOpenWithOptions(dir, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return co, nil
	}
	if head, err := repo.Head(); err == nil {
		co.Ref = head.Name().Short()
		co.Commit = head.Hash().String()
	}
	return co, nil
}
