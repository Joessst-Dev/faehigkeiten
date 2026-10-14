// Package update checks tracked skills against their sources and applies updates.
package update

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Masterminds/semver/v3"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// Fetcher makes a source available on disk.
type Fetcher interface {
	Fetch(ctx context.Context, src, ref string, refresh bool) (*source.Checkout, error)
}

// Status is the update state of one installed skill.
type Status struct {
	Entry lock.Entry
	// Current is the installed version or hash.
	Current string
	// Latest is the upstream version or hash.
	Latest    string
	Available bool
	// Modified reports local edits to the installed copy; applying an update
	// overwrites them.
	Modified bool
	Err      error

	skill    skill.Skill
	checkout *source.Checkout
}

// Check fetches the sources of all tracked entries of the installer's
// lockfile and compares them with the installed state. Untracked entries are
// skipped.
func Check(ctx context.Context, f Fetcher, in *install.Installer) []Status {
	lf := in.Lock
	type key struct{ src, ref string }
	checkouts := map[key]*source.Checkout{}
	fetchErrs := map[key]error{}

	var out []Status
	for _, e := range lf.Skills {
		if e.Tracking == lock.TrackNone || e.Tracking == "" || e.Source == "" {
			continue
		}
		st := Status{Entry: e}
		if modified, err := in.Modified(e); err == nil {
			st.Modified = modified
		}
		k := key{e.Source, e.Ref}
		co, seen := checkouts[k]
		err := fetchErrs[k]
		if !seen && err == nil {
			co, err = f.Fetch(ctx, e.Source, e.Ref, true)
			checkouts[k], fetchErrs[k] = co, err
		}
		if err != nil {
			st.Err = err
			out = append(out, st)
			continue
		}
		st.checkout = co
		compare(&st, co)
		out = append(out, st)
	}
	return out
}

// Updatable reports whether Apply can update the skill: an update is
// available and the skill is not pinned. Local modifications are not
// considered; callers decide whether to overwrite them.
func (st Status) Updatable() bool {
	return st.Err == nil && st.Available && !st.Entry.Pinned
}

// Notes describes the state of a successfully checked skill, most important
// first, e.g. ["pinned", "newer version available", "locally modified"].
func (st Status) Notes() []string {
	var out []string
	switch {
	case st.Entry.Pinned:
		out = append(out, "pinned")
		if st.Available {
			out = append(out, "newer version available")
		}
	case st.Available:
		out = append(out, "update available")
	default:
		out = append(out, "up to date")
	}
	if st.Modified {
		out = append(out, "locally modified")
	}
	return out
}

func compare(st *Status, co *source.Checkout) {
	e := st.Entry
	dir, err := co.SkillDir(e.Path)
	if err != nil {
		st.Err = fmt.Errorf("skill %s: %w", e.Name, err)
		return
	}
	s, err := skill.ParseFile(filepath.Join(dir, skill.FileName))
	if err != nil {
		st.Err = fmt.Errorf("skill %s no longer found at %s: %w", e.Name, e.Path, err)
		return
	}
	s.Dir, s.Path = dir, e.Path
	st.skill = s

	switch e.Tracking {
	case lock.TrackVersion:
		st.Current, st.Latest = e.Version, s.Version
		if s.Version == "" {
			st.Err = fmt.Errorf("skill %s no longer declares a version; reinstall with hash tracking", e.Name)
			return
		}
		st.Available = Newer(s.Version, e.Version)
	case lock.TrackHash:
		h, err := skill.Hash(dir)
		if err != nil {
			st.Err = err
			return
		}
		st.Current, st.Latest = e.Hash, h
		st.Available = h != e.Hash
	}
}

// Newer reports whether latest is newer than current. Semantic versions are
// compared numerically; anything else counts as newer when it differs.
func Newer(latest, current string) bool {
	l, errL := semver.NewVersion(latest)
	c, errC := semver.NewVersion(current)
	if errL == nil && errC == nil {
		return l.GreaterThan(c)
	}
	return latest != current
}

// ErrPinned is returned when applying an update to a pinned skill.
var ErrPinned = errors.New("skill is pinned")

// Apply reinstalls the skill of an available update with its recorded agents
// and tracking mode. Pinned skills are refused; see Unpin. The lockfile is
// not saved.
func Apply(in *install.Installer, st Status) (lock.Entry, error) {
	if st.Entry.Pinned {
		return lock.Entry{}, fmt.Errorf("%w: %s is pinned at %s", ErrPinned, st.Entry.Name, st.Entry.Version)
	}
	if !st.Available || st.checkout == nil {
		return lock.Entry{}, fmt.Errorf("no update available for %s", st.Entry.Name)
	}
	// Agents defined only in someone else's configuration are skipped here but
	// kept in the lockfile.
	var agents []agent.Agent
	for _, id := range st.Entry.Agents {
		if a, ok := in.Registry.Get(id); ok {
			agents = append(agents, a)
		}
	}
	if len(agents) == 0 {
		return lock.Entry{}, fmt.Errorf("none of the agents of %s (%s) are known on this machine", st.Entry.Name, strings.Join(st.Entry.Agents, ", "))
	}
	e, err := in.Install(install.Request{
		Skill:    st.skill,
		Checkout: st.checkout,
		Agents:   agents,
		Tracking: st.Entry.Tracking,
		// Keep the installed directory name even if upstream renamed the skill.
		Name:  st.Entry.Name,
		Force: true,
	})
	if err != nil {
		return lock.Entry{}, err
	}
	e.Agents = st.Entry.Agents
	in.Lock.Upsert(e)
	return e, nil
}

// UnpinChanges reports whether Unpin reinstalls the skill: upstream differs
// from the pinned version, newer or not, or has other content under the same
// version (fixes made without a version bump).
func (st Status) UnpinChanges() bool {
	if st.Err != nil || st.checkout == nil {
		return false
	}
	if st.Current != st.Latest {
		return true
	}
	// Hashed only here: checks never need it. An unreadable upstream is
	// treated as a change, so reinstalling reports the error.
	h, err := skill.Hash(st.skill.Dir)
	return err != nil || h != st.Entry.Hash
}

// Unpin removes the pin of a skill. When upstream differs from the pinned
// version, even when it is older, the skill is reinstalled from upstream and
// updated is true. The lockfile is not saved.
func Unpin(in *install.Installer, st Status) (e lock.Entry, updated bool, err error) {
	st.Entry.Pinned = false
	if !st.UnpinChanges() {
		in.Lock.Upsert(st.Entry)
		return st.Entry, false, nil
	}
	st.Available = true
	e, err = Apply(in, st)
	return e, err == nil, err
}
