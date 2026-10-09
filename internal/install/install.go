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
		d, err := t.dir(a)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// dir returns the skill directory of a for this target. In a project, the
// directory must not be reached through a symlink: a cloned repository could
// otherwise commit e.g. .claude/skills -> ../.. and redirect installs and
// removals to anywhere on disk.
func (t Target) dir(a agent.Agent) (string, error) {
	d, err := a.Dir(t.Scope, t.ProjectRoot, t.Home)
	if err != nil {
		return "", err
	}
	if t.Scope == agent.ScopeProject {
		if err := noSymlinks(t.ProjectRoot, d); err != nil {
			return "", fmt.Errorf("agent %s: %w", a.ID, err)
		}
	}
	return d, nil
}

// noSymlinks fails if any existing path component of dir below root is a
// symlink. Directories configured outside root are left to the user.
func noSymlinks(root, dir string) error {
	rel, err := filepath.Rel(root, dir)
	if err != nil || !filepath.IsLocal(rel) {
		return nil
	}
	p := root
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		p = filepath.Join(p, part)
		fi, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to use %s: it is a symlink", p)
		}
	}
	return nil
}

// Request describes a single skill installation.
type Request struct {
	Skill    skill.Skill
	Checkout *source.Checkout
	Agents   []agent.Agent
	Tracking lock.Tracking
	// Name overrides the directory name derived from the skill's name. Updates
	// use it to keep the name recorded in the lockfile.
	Name string
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
	if req.Name != "" {
		if err := lock.ValidateName(req.Name); err != nil {
			return lock.Entry{}, err
		}
		name = req.Name
	}
	dirs, err := in.Target.Dirs(req.Agents)
	if err != nil {
		return lock.Entry{}, err
	}

	prev, managed := in.Lock.Get(name)
	// Local-only entries (adopted without a source) may be replaced by any source.
	if managed && prev.Source != "" && prev.Source != req.Checkout.URL && !req.Force {
		return lock.Entry{}, fmt.Errorf("%w: %s is installed from %s", ErrConflict, name, prev.Source)
	}
	var prevDirs []string
	if managed {
		if prevDirs, err = in.entryDirs(prev); err != nil {
			return lock.Entry{}, err
		}
	}
	if !req.Force {
		for _, d := range dirs {
			target := filepath.Join(d, name)
			if exists(target) && !slices.Contains(prevDirs, d) {
				return lock.Entry{}, fmt.Errorf("%w: %s", ErrConflict, target)
			}
		}
	}

	targets := make([]string, len(dirs))
	for i, d := range dirs {
		targets[i] = filepath.Join(d, name)
	}
	if err := replaceDirs(req.Skill.Dir, targets); err != nil {
		return lock.Entry{}, err
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
	if req.Tracking == lock.TrackVersion {
		entry.Version = req.Skill.Version
	}
	if req.Tracking != lock.TrackNone {
		// The hash describes the installed content. Hash tracking compares it
		// with upstream; all tracked skills use it to detect local edits.
		if entry.Hash, err = skill.Hash(req.Skill.Dir); err != nil {
			return lock.Entry{}, err
		}
	}
	in.Lock.Upsert(entry)
	return entry, nil
}

// Modified reports whether an installed copy of the skill differs from the
// content recorded in the lockfile, i.e. it was edited locally. Entries
// without a recorded hash (untracked skills) are never reported.
func (in *Installer) Modified(e lock.Entry) (bool, error) {
	if e.Hash == "" {
		return false, nil
	}
	dirs, err := in.entryDirs(e)
	if err != nil {
		return false, err
	}
	for _, d := range dirs {
		dir := filepath.Join(d, e.Name)
		if !exists(dir) {
			continue // a missing copy is restored by the next install
		}
		h, err := skill.Hash(dir)
		if err != nil {
			return false, err
		}
		if h != e.Hash {
			return true, nil
		}
	}
	return false, nil
}

// Uninstall removes the named skill from every agent directory and from the
// lockfile. The lockfile is not saved.
func (in *Installer) Uninstall(name string) error {
	e, ok := in.Lock.Get(name)
	if !ok {
		return fmt.Errorf("skill %s is not installed", name)
	}
	dirs, err := in.entryDirs(e)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		if err := os.RemoveAll(filepath.Join(d, e.Name)); err != nil {
			return err
		}
	}
	in.Lock.Remove(name)
	return nil
}

// entryDirs returns the agent directories recorded for an entry, skipping
// agents that are no longer known or no longer support the target's scope.
// Unsafe directories are still an error.
func (in *Installer) entryDirs(e lock.Entry) ([]string, error) {
	var dirs []string
	for _, id := range e.Agents {
		a, ok := in.Registry.Get(id)
		if !ok {
			continue
		}
		if _, err := a.Dir(in.Target.Scope, in.Target.ProjectRoot, in.Target.Home); err != nil {
			continue
		}
		d, err := in.Target.dir(a)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(dirs, d) {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
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

// replaceDirs copies src to every destination. All copies are first written
// to temporary siblings and only swapped in once every copy succeeded, so a
// failed copy leaves all destinations untouched.
func replaceDirs(src string, dsts []string) error {
	tmps := make([]string, 0, len(dsts))
	defer func() {
		for _, t := range tmps {
			os.RemoveAll(t)
		}
	}()
	for _, dst := range dsts {
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp, err := os.MkdirTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".tmp-")
		if err != nil {
			return err
		}
		tmps = append(tmps, tmp)
		if err := copyTree(src, tmp); err != nil {
			return err
		}
	}
	for i, dst := range dsts {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := os.Rename(tmps[i], dst); err != nil {
			return err
		}
	}
	return nil
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
		dirs, _ := in.entryDirs(e)
		for _, d := range dirs {
			managed[filepath.Join(d, e.Name)] = true
		}
	}
	byName := map[string]*Found{}
	var names []string
	scanned := map[string]bool{}
	for _, a := range in.Registry.All() {
		root, err := in.Target.dir(a)
		if err != nil {
			continue
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, de := range entries {
			dir := filepath.Join(root, de.Name())
			// Names a lockfile cannot hold (e.g. hidden ones) cannot be managed.
			if managed[dir] || lock.ValidateName(de.Name()) != nil {
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

// AdoptRequest turns an unmanaged skill into a managed one without touching
// its files.
type AdoptRequest struct {
	Found Found
	// Checkout and Upstream identify the source to track. Both nil adopts the
	// skill as local-only, which records it without update tracking.
	Checkout *source.Checkout
	Upstream *skill.Skill
	Tracking lock.Tracking
}

// Adopt records an unmanaged skill in the lockfile. The installed files are
// kept as they are; the recorded version or hash describes the local copy, so
// an update check reports a difference to upstream right away. The lockfile
// is not saved.
func (in *Installer) Adopt(req AdoptRequest) (lock.Entry, error) {
	f := req.Found
	if err := lock.ValidateName(f.Name); err != nil {
		return lock.Entry{}, err
	}
	if len(f.Dirs) == 0 {
		return lock.Entry{}, fmt.Errorf("skill %s has no directories", f.Name)
	}
	if _, ok := in.Lock.Get(f.Name); ok {
		return lock.Entry{}, fmt.Errorf("%w: %s is already managed", ErrConflict, f.Name)
	}
	entry := lock.Entry{Name: f.Name, Agents: f.Agents, Tracking: lock.TrackNone, InstalledAt: in.now()}
	if req.Checkout == nil || req.Upstream == nil {
		if req.Tracking != "" && req.Tracking != lock.TrackNone {
			return lock.Entry{}, fmt.Errorf("local-only skill %s cannot be tracked for updates", f.Name)
		}
		in.Lock.Upsert(entry)
		return entry, nil
	}

	local, err := skill.Hash(f.Dirs[0])
	if err != nil {
		return lock.Entry{}, err
	}
	upstream, err := skill.Hash(req.Upstream.Dir)
	if err != nil {
		return lock.Entry{}, err
	}
	entry.Source = req.Checkout.URL
	entry.Path = req.Upstream.Path
	entry.Ref = req.Checkout.Ref
	if local == upstream {
		// Only claim a commit when the local copy matches it exactly.
		entry.Commit = req.Checkout.Commit
	}
	entry.Tracking = req.Tracking
	if entry.Tracking == "" {
		entry.Tracking = DefaultTracking(*req.Upstream)
	}
	switch entry.Tracking {
	case lock.TrackVersion:
		if !req.Upstream.HasVersion() {
			return lock.Entry{}, fmt.Errorf("upstream skill %s has no version; use hash tracking instead", req.Upstream.Name)
		}
		entry.Version = f.Version
		if entry.Version == "" {
			// Unknown local version: any upstream version counts as an update.
			entry.Version = "0.0.0"
		}
	case lock.TrackHash, lock.TrackNone:
	default:
		return lock.Entry{}, fmt.Errorf("invalid tracking mode %q", entry.Tracking)
	}
	if entry.Tracking != lock.TrackNone {
		entry.Hash = local
	}
	in.Lock.Upsert(entry)
	return entry, nil
}
