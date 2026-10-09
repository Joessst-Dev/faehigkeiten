// Package source fetches skill repositories into a local cache.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
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
	if lower := strings.ToLower(in); strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "git://") {
		return "", fmt.Errorf("refusing insecure source %q: use https:// or ssh", input)
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
		if _, after, ok := strings.Cut(s, ":"); ok {
			return after
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
	// The readable part may collide (a/b vs a_b); the hash keeps keys unique.
	sum := sha256.Sum256([]byte(src + "\x00" + ref))
	key := unsafeChars.ReplaceAllString(strings.TrimPrefix(src, "https://"), "_")
	if ref != "" {
		key += "@" + unsafeChars.ReplaceAllString(ref, "_")
	}
	return filepath.Join(m.CacheDir, "repos", key+"-"+hex.EncodeToString(sum[:4]))
}

// Fetch makes src available on disk. Plain local directories are used in
// place. Git sources are shallow-cloned into the cache; an existing clone is
// reused as is, or updated to the latest commit when refresh is set (falling
// back to a fresh clone if the update fails).
func (m *Manager) Fetch(ctx context.Context, src, ref string, refresh bool) (*Checkout, error) {
	src, err := Normalize(src)
	if err != nil {
		return nil, err
	}
	if IsLocal(src) {
		return localCheckout(src)
	}
	dir := m.cachePath(src, ref)
	if ref != "" {
		// Installs record the branch they got (e.g. "main") even when they
		// fetched the default branch; reuse that clone instead of cloning the
		// same branch a second time under its name.
		def := m.cachePath(src, "")
		if co, err := openCheckout(src, def); err == nil && co.Ref == ref {
			dir = def
		}
	}
	defer m.lock(dir)()

	if co, err := openCheckout(src, dir); err == nil {
		if !refresh {
			return co, nil
		}
		if err := pull(ctx, dir); err == nil {
			return openCheckout(src, dir)
		}
		// Fall back to a fresh clone if the incremental update failed.
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	// A unique temp dir keeps concurrent processes from clobbering each other.
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := clone(ctx, src, ref, tmp); err != nil {
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

// pull updates a shallow single-branch clone to the latest commit of its
// branch. Detached checkouts (tags) are not updated and report an error so
// the caller re-clones.
func pull(ctx context.Context, dir string) error {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return err
	}
	head, err := repo.Head()
	if err != nil {
		return err
	}
	if !head.Name().IsBranch() {
		return errors.New("not on a branch")
	}
	branch := head.Name().Short()
	remoteRef := plumbing.NewRemoteReferenceName("origin", branch)
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RefSpecs: []config.RefSpec{config.RefSpec("+" + head.Name().String() + ":" + remoteRef.String())},
		Depth:    1,
		Tags:     git.NoTags,
		Force:    true,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	ref, err := repo.Reference(remoteRef, true)
	if err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.Reset(&git.ResetOptions{Commit: ref.Hash(), Mode: git.HardReset}); err != nil {
		return err
	}
	// Remove files that upstream deleted but the reset left behind.
	return wt.Clean(&git.CleanOptions{Dir: true})
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

// SkillDir returns the directory at the slash-separated path rel inside the
// checkout. It fails when the path resolves outside the checkout, e.g. through
// a symlink committed to the repository.
func (c *Checkout) SkillDir(rel string) (string, error) {
	dir := filepath.Join(c.Dir, filepath.FromSlash(rel))
	root, err := filepath.EvalSymlinks(c.Dir)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if r, err := filepath.Rel(root, resolved); err != nil || (r != "." && !filepath.IsLocal(r)) {
		return "", fmt.Errorf("%s points outside of %s", rel, DisplayName(c.URL))
	}
	return dir, nil
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
