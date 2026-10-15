package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
)

// Version is a version of a skill found in the history of its SKILL.md.
type Version struct {
	// Version is the frontmatter version.
	Version string
	// Commit is the newest commit of the skill that still had this version.
	Commit string
	// Date is the commit time of Commit.
	Date time.Time
	// Latest is set on the version the tip of the ref declares. It is unset
	// everywhere when the skill no longer declares a version there.
	Latest bool
}

// history is a repository with the full history of one ref of a source.
type history struct {
	repo *git.Repository
	// prefix is the path of the source inside the repository, for local
	// directories below a repository root ("" otherwise).
	prefix string
	// ref is the followed branch or tag.
	ref string
}

// Versions lists the versions of the skill at the slash-separated path in the
// branch or tag ref of src, newest first. Every version maps to the newest
// commit that still declared it, so fixes made without a version bump are
// included. Commits without a parseable version are skipped. With refresh,
// the cached history is updated first; if that fails, the cached history is
// used.
func (m *Manager) Versions(ctx context.Context, src, ref, skillPath string, refresh bool) ([]Version, error) {
	src, err := Normalize(src)
	if err != nil {
		return nil, err
	}
	h, unlock, err := m.history(ctx, src, ref, refresh, false)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return h.versions(skillPath)
}

// FetchCommit makes the skill at the slash-separated path skillPath of
// commit in the branch or tag ref of src available on disk. Only the skill's
// directory is written, once per commit and skill, into a cache directory that
// is reused afterwards; the returned checkout's Dir is the repository root of
// that tree. Symbolic links are not written.
func (m *Manager) FetchCommit(ctx context.Context, src, ref, commit, skillPath string) (*Checkout, error) {
	src, err := Normalize(src)
	if err != nil {
		return nil, err
	}
	if !plumbing.IsHash(commit) {
		return nil, fmt.Errorf("invalid commit %q", commit)
	}
	// Trees cached under the old key lack the repository's license files.
	dir := m.cachePath(src, commit+":"+skillPath+":licenses")
	if !IsLocal(src) && ref != "" {
		// Cached trees need no history; only the ref has to be known.
		if _, err := os.Stat(dir); err == nil {
			return &Checkout{URL: src, Ref: ref, Commit: commit, Dir: dir}, nil
		}
	}
	h, unlock, err := m.history(ctx, src, ref, false, true)
	if err != nil {
		return nil, err
	}
	// unlock is replaced when the history is refreshed below.
	defer func() { unlock() }()
	co := &Checkout{URL: src, Ref: h.ref, Commit: commit, Dir: filepath.Join(dir, filepath.FromSlash(h.prefix))}
	if _, err := os.Stat(dir); err == nil {
		return co, nil
	}
	c, err := h.repo.CommitObject(plumbing.NewHash(commit))
	if errors.Is(err, plumbing.ErrObjectNotFound) && !IsLocal(src) {
		// The commit may be newer than the cached history.
		unlock()
		unlock = func() {}
		if h, unlock, err = m.history(ctx, src, ref, true, true); err != nil {
			unlock = func() {}
			return nil, err
		}
		c, err = h.repo.CommitObject(plumbing.NewHash(commit))
	}
	if err != nil {
		return nil, fmt.Errorf("commit %s of %s: %w", commit, DisplayName(src), err)
	}
	if err := writeTree(c, path.Join(h.prefix, skillPath), h.prefix, dir); err != nil {
		return nil, err
	}
	return co, nil
}

// history opens the full history of ref. Local directories use their own
// repository; remote sources keep a bare clone in the cache, which is updated
// when refresh is set. Callers that only read known commits pass anyBranch,
// so local repositories may have another branch checked out. The returned
// function releases the clone.
func (m *Manager) history(ctx context.Context, src, ref string, refresh, anyBranch bool) (*history, func(), error) {
	if IsLocal(src) {
		h, err := localHistory(src, ref, anyBranch)
		return h, func() {}, err
	}
	dir := m.cachePath(src, ref) + ".history"
	if ref != "" {
		// Pins record the branch they got (e.g. "main") even when they read
		// the default branch; reuse that history instead of cloning it again.
		def := m.cachePath(src, "") + ".history"
		unlockDef := m.lock(def)
		if h, err := openHistory(def, ""); err == nil && h.ref == ref {
			dir = def
		}
		unlockDef()
	}
	unlock := m.lock(dir)
	h, err := openHistory(dir, ref)
	if err == nil && refresh {
		if err := fetchHistory(ctx, h.repo); isAuthErr(err) {
			unlock()
			return nil, nil, fmt.Errorf("update %s: %w", src, err)
		}
		// Other failures (e.g. being offline) keep the cached history; a
		// fresh clone would most likely fail the same way.
	}
	if err != nil {
		// Missing or broken; clone it again.
		if err := m.cloneHistory(ctx, src, ref, dir); err != nil {
			unlock()
			return nil, nil, err
		}
		if h, err = openHistory(dir, ref); err != nil {
			unlock()
			return nil, nil, err
		}
	}
	return h, unlock, nil
}

func (m *Manager) cloneHistory(ctx context.Context, src, ref, dir string) error {
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := cloneRepo(ctx, src, ref, tmp, true, 0); err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Rename(tmp, dir)
}

func openHistory(dir, ref string) (*history, error) {
	repo, err := git.PlainOpen(dir)
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	h := &history{repo: repo, ref: ref}
	if head.Name().IsBranch() || ref == "" {
		h.ref = head.Name().Short()
	}
	return h, nil
}

// fetchHistory updates the branch of a bare history clone. Tags are not
// updated.
func fetchHistory(ctx context.Context, repo *git.Repository) error {
	head, err := repo.Head()
	if err != nil {
		return err
	}
	if !head.Name().IsBranch() {
		return nil
	}
	return newAuthenticator(ctx, remoteURL(repo)).run(func(auth transport.AuthMethod) error {
		err := repo.FetchContext(ctx, &git.FetchOptions{
			RefSpecs: []config.RefSpec{config.RefSpec("+" + head.Name().String() + ":" + head.Name().String())},
			Tags:     git.NoTags,
			Force:    true,
			Auth:     auth,
		})
		if errors.Is(err, git.NoErrAlreadyUpToDate) {
			return nil
		}
		return err
	})
}

// localHistory opens the repository containing the local directory src.
// Unless anyBranch is set, ref must be its checked-out branch.
func localHistory(src, ref string, anyBranch bool) (*history, error) {
	repo, err := git.PlainOpenWithOptions(src, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("%s is not a git repository, so it has no version history", src)
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	if ref != "" && ref != head.Name().Short() && !anyBranch {
		return nil, fmt.Errorf("%s has %s checked out, not %s", src, head.Name().Short(), ref)
	}
	wt, err := repo.Worktree()
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(wt.Filesystem.Root())
	if err != nil {
		return nil, err
	}
	dir, err := filepath.EvalSymlinks(src)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil {
		return nil, err
	}
	h := &history{repo: repo, ref: head.Name().Short()}
	if ref != "" {
		h.ref = ref
	}
	if rel != "." {
		h.prefix = filepath.ToSlash(rel)
	}
	return h, nil
}

func (h *history) versions(skillPath string) ([]Version, error) {
	dir := path.Join(h.prefix, skillPath)
	if dir == "." {
		dir = ""
	}
	file := path.Join(dir, skill.FileName)
	head, err := h.repo.Head()
	if err != nil {
		return nil, err
	}
	tip := ""
	if c, err := h.repo.CommitObject(head.Hash()); err == nil {
		tip = fileVersion(c, file)
	}
	iter, err := h.repo.Log(&git.LogOptions{
		From: head.Hash(),
		// Newest first also across merges, so every version maps to its
		// newest commit.
		Order: git.LogOrderCommitterTime,
		PathFilter: func(p string) bool {
			return dir == "" || strings.HasPrefix(p, dir+"/")
		},
	})
	if err != nil {
		return nil, err
	}
	defer iter.Close()

	var out []Version
	seen := map[string]bool{}
	err = iter.ForEach(func(c *object.Commit) error {
		v := fileVersion(c, file)
		if v == "" || seen[v] {
			return nil
		}
		seen[v] = true
		out = append(out, Version{Version: v, Commit: c.Hash.String(), Date: c.Committer.When, Latest: v == tip})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// fileVersion returns the version declared by the SKILL.md file of commit
// c, or "" when it is missing, unparseable or has no version.
func fileVersion(c *object.Commit, file string) string {
	f, err := c.File(file)
	if err != nil {
		return ""
	}
	content, err := f.Contents()
	if err != nil {
		return ""
	}
	s, err := skill.Parse([]byte(content))
	if err != nil {
		return ""
	}
	return s.Version
}

// writeTree writes the files below the slash-separated directory sub of
// commit c ("" or "." for all) into dir, keeping their paths relative to the
// repository root. The license files directly in the directory licenses are
// written too, so the license of a skill without its own can be found. The
// directory is created atomically.
func writeTree(c *object.Commit, sub, licenses, dir string) error {
	root, err := c.Tree()
	if err != nil {
		return err
	}
	tree := root
	if sub = path.Clean(sub); sub != "." {
		if tree, err = tree.Tree(sub); err != nil {
			return fmt.Errorf("%s at commit %s: %w", sub, c.Hash, err)
		}
	} else {
		sub = ""
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dir), "."+filepath.Base(dir)+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	err = tree.Files().ForEach(func(f *object.File) error {
		if f.Mode != filemode.Regular && f.Mode != filemode.Executable {
			// Symlinks could point anywhere and are not installed anyway.
			return nil
		}
		name := path.Join(sub, f.Name)
		if !filepath.IsLocal(filepath.FromSlash(name)) {
			return fmt.Errorf("refusing unsafe path %q in commit %s", name, c.Hash)
		}
		return writeBlob(f, filepath.Join(tmp, filepath.FromSlash(name)))
	})
	if err != nil {
		return err
	}
	if licenses = path.Clean(licenses); licenses != path.Clean(sub) {
		if err := writeLicenses(c, root, licenses, tmp); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			// Another process wrote the same commit first.
			return nil
		}
		return err
	}
	return nil
}

// writeLicenses writes the license files directly in the directory dir of
// commit c below tmp.
func writeLicenses(c *object.Commit, root *object.Tree, dir, tmp string) error {
	tree := root
	if dir != "." {
		var err error
		if tree, err = root.Tree(dir); err != nil {
			// The source directory itself is missing; nothing to add.
			return nil
		}
	}
	for _, e := range tree.Entries {
		if e.Mode != filemode.Regular && e.Mode != filemode.Executable || !skill.IsLicenseFile(e.Name) {
			continue
		}
		f, err := tree.TreeEntryFile(&e)
		if err != nil {
			return fmt.Errorf("%s at commit %s: %w", path.Join(dir, e.Name), c.Hash, err)
		}
		name := filepath.Join(tmp, filepath.FromSlash(path.Join(dir, e.Name)))
		if err := writeBlob(f, name); err != nil && !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	return nil
}

func writeBlob(f *object.File, p string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	perm := os.FileMode(0o644)
	if f.Mode == filemode.Executable {
		perm = 0o755
	}
	r, err := f.Reader()
	if err != nil {
		return err
	}
	defer r.Close()
	out, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, r); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
