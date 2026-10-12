// Package testutil contains helpers shared by the test suites.
package testutil

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// WriteFiles creates every file in files (slash-separated relative path → content) below root.
func WriteFiles(root string, files map[string]string) error {
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// FileURL returns a file:// URL for a local path, including Windows drive paths.
func FileURL(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

// SkillMD renders a SKILL.md with the given name, description and optional version.
func SkillMD(name, description, version string) string {
	s := "---\nname: " + name + "\ndescription: " + description + "\n"
	if version != "" {
		s += "version: " + version + "\n"
	}
	return s + "---\n\n# " + name + "\n"
}

// InitRepo creates a git repository at dir containing files and commits them.
func InitRepo(dir string, files map[string]string) (*git.Repository, error) {
	repo, err := git.PlainInit(dir, false)
	if err != nil {
		return nil, err
	}
	return repo, Commit(repo, dir, files, "initial")
}

// Commit writes files into the worktree at dir and commits all changes.
func Commit(repo *git.Repository, dir string, files map[string]string, msg string) error {
	if err := WriteFiles(dir, files); err != nil {
		return err
	}
	wt, err := repo.Worktree()
	if err != nil {
		return err
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return err
	}
	_, err = wt.Commit(msg, &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@example.com", When: time.Now()},
	})
	return err
}

// SeedIndex adds repos (typically local directories) to cfg, saves it, and
// stores their skill index in cacheDir. The builtin repositories are marked
// as indexed, so lookups never reach them.
func SeedIndex(cfg *config.Config, cacheDir string, repos ...registry.Repo) error {
	for _, r := range repos {
		cfg.AddRepo(config.Repo{Name: r.Name, URL: r.URL, Ref: r.Ref})
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	idx := registry.Build(context.Background(), source.NewManager(cacheDir), repos, false, nil)
	idx = idx.MarkScanned(registry.Builtin()...)
	return idx.Save(registry.IndexPath(cacheDir))
}
