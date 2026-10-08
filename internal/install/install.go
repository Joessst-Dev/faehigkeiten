// Package install copies skills into agent skill directories and keeps the
// lockfile in sync.
package install

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// ErrConflict is returned when the target directory exists but is not managed
// by faehigkeiten for the same source.
var ErrConflict = errors.New("skill directory already exists")

// Target describes where skills get installed.
type Target struct {
	Scope       agent.Scope
	ProjectRoot string
	Home        string
}

// Dirs returns the distinct skill directories of agents for this target.
func (t Target) Dirs(agents []agent.Agent) ([]string, error) {
	var dirs []string
	for _, a := range agents {
		d, err := a.Dir(t.Scope, t.ProjectRoot, t.Home)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// Request describes a single skill installation.
type Request struct {
	Skill    skill.Skill
	Checkout *source.Checkout
	Agents   []agent.Agent
	Tracking lock.Tracking
	// Force overwrites existing directories that are not managed by this lockfile.
	Force bool
}

// DefaultTracking is version tracking for versioned skills and no tracking
// otherwise; hash tracking is an explicit opt-in.
func DefaultTracking(s skill.Skill) lock.Tracking {
	if s.HasVersion() {
		return lock.TrackVersion
	}
	return lock.TrackNone
}

// AvailableTracking lists the tracking modes a skill supports.
func AvailableTracking(s skill.Skill) []lock.Tracking {
	if s.HasVersion() {
		return []lock.Tracking{lock.TrackVersion, lock.TrackHash, lock.TrackNone}
	}
	return []lock.Tracking{lock.TrackHash, lock.TrackNone}
}

var invalidName = regexp.MustCompile(`[^a-z0-9._-]+`)

// DirName turns a skill name into a safe directory name.
func DirName(name string) string {
	n := invalidName.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	n = strings.Trim(n, "-.")
	if n == "" {
		return "skill"
	}
	return n
}

// Installer installs skills for one target and lockfile.
type Installer struct {
	Target   Target
	Lock     *lock.File
	Registry *agent.Registry
	Now      func() time.Time
}

// Install copies the skill to every agent directory and records it in the
// lockfile. The lockfile is not saved.
func (in *Installer) Install(req Request) (lock.Entry, error) {
	if len(req.Agents) == 0 {
		return lock.Entry{}, errors.New("no agents selected")
	}
	if req.Tracking == "" {
		req.Tracking = DefaultTracking(req.Skill)
	}
	if req.Tracking == lock.TrackVersion && !req.Skill.HasVersion() {
		return lock.Entry{}, fmt.Errorf("skill %s has no version; use hash tracking instead", req.Skill.Name)
	}
	name := DirName(req.Skill.Name)
	dirs, err := in.Target.Dirs(req.Agents)
	if err != nil {
		return lock.Entry{}, err
	}

	prev, managed := in.Lock.Get(name)
	if managed && prev.Source != req.Checkout.URL && !req.Force {
		return lock.Entry{}, fmt.Errorf("%w: %s is installed from %s", ErrConflict, name, prev.Source)
	}
	var prevDirs []string
	if managed {
		prevDirs = in.entryDirs(prev)
	}
	if !req.Force {
		for _, d := range dirs {
			target := filepath.Join(d, name)
			if exists(target) && !slices.Contains(prevDirs, d) {
				return lock.Entry{}, fmt.Errorf("%w: %s", ErrConflict, target)
			}
		}
	}

	for _, d := range dirs {
		if err := replaceDir(req.Skill.Dir, filepath.Join(d, name)); err != nil {
			return lock.Entry{}, err
		}
	}
	// Remove copies for agents that are no longer selected.
	for _, d := range prevDirs {
		if !slices.Contains(dirs, d) {
			if err := os.RemoveAll(filepath.Join(d, name)); err != nil {
				return lock.Entry{}, err
			}
		}
	}

	entry := lock.Entry{
		Name:        name,
		Source:      req.Checkout.URL,
		Path:        req.Skill.Path,
		Ref:         req.Checkout.Ref,
		Commit:      req.Checkout.Commit,
		Tracking:    req.Tracking,
		InstalledAt: in.now(),
	}
	for _, a := range req.Agents {
		entry.Agents = append(entry.Agents, a.ID)
	}
	switch req.Tracking {
	case lock.TrackVersion:
		entry.Version = req.Skill.Version
	case lock.TrackHash:
		if entry.Hash, err = skill.Hash(req.Skill.Dir); err != nil {
			return lock.Entry{}, err
		}
	}
	in.Lock.Upsert(entry)
	return entry, nil
}

// Uninstall removes the named skill from every agent directory and from the
// lockfile. The lockfile is not saved.
func (in *Installer) Uninstall(name string) error {
	e, ok := in.Lock.Get(name)
	if !ok {
		return fmt.Errorf("skill %s is not installed", name)
	}
	for _, d := range in.entryDirs(e) {
		if err := os.RemoveAll(filepath.Join(d, e.Name)); err != nil {
			return err
		}
	}
	in.Lock.Remove(name)
	return nil
}

// entryDirs returns the agent directories recorded for an entry, skipping
// agents that are no longer known.
func (in *Installer) entryDirs(e lock.Entry) []string {
	var agents []agent.Agent
	for _, id := range e.Agents {
		if a, ok := in.Registry.Get(id); ok {
			agents = append(agents, a)
		}
	}
	dirs, _ := in.Target.Dirs(agents)
	return dirs
}

func (in *Installer) now() time.Time {
	if in.Now != nil {
		return in.Now()
	}
	return time.Now().UTC().Truncate(time.Second)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// replaceDir copies src to dst via a temporary sibling so a failed copy never
// leaves a half-written skill behind.
func replaceDir(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	if err := copyTree(src, tmp); err != nil {
		return err
	}
	if err := os.RemoveAll(dst); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if p != src && d.Name() == ".git" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return copyFile(p, target, info.Mode().Perm())
	})
}

func copyFile(src, dst string, perm fs.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm|0o200)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// Found is a skill present in agent directories but not managed by the lockfile,
// e.g. one copied by hand or installed with another tool.
type Found struct {
	Name        string
	Description string
	Version     string
	// Agents lists the IDs of agents whose directory contains the skill.
	Agents []string
	// Dirs are the skill directories on disk.
	Dirs []string
}

// Unmanaged scans the skill directories of all known agents for the target and
// returns skills that are not recorded in the lockfile, sorted by name.
func (in *Installer) Unmanaged() []Found {
	managed := map[string]bool{}
	for _, e := range in.Lock.Skills {
		for _, d := range in.entryDirs(e) {
			managed[filepath.Join(d, e.Name)] = true
		}
	}
	byName := map[string]*Found{}
	var names []string
	scanned := map[string]bool{}
	for _, a := range in.Registry.All() {
		root, err := a.Dir(in.Target.Scope, in.Target.ProjectRoot, in.Target.Home)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range entries {
			dir := filepath.Join(root, de.Name())
			if managed[dir] || strings.HasPrefix(de.Name(), ".") {
				continue
			}
			// Stat follows symlinks, which other installers commonly use.
			if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
				continue
			}
			s, err := skill.ParseFile(filepath.Join(dir, skill.FileName))
			if err != nil {
				continue
			}
			f, ok := byName[de.Name()]
			if !ok {
				f = &Found{Name: de.Name(), Description: s.Description, Version: s.Version}
				byName[de.Name()] = f
				names = append(names, de.Name())
			}
			f.Agents = append(f.Agents, a.ID)
			if !scanned[dir] {
				scanned[dir] = true
				f.Dirs = append(f.Dirs, dir)
			}
		}
	}
	slices.Sort(names)
	out := make([]Found, 0, len(names))
	for _, n := range names {
		out = append(out, *byName[n])
	}
	return out
}

// RemoveFound deletes the directories of an unmanaged skill.
func RemoveFound(f Found) error {
	for _, d := range f.Dirs {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	return nil
}
