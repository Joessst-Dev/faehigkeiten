// Package update checks tracked skills against their sources and applies updates.
package update

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Masterminds/semver/v3"

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
	Err       error

	skill    skill.Skill
	checkout *source.Checkout
}

// Check fetches the sources of all tracked entries and compares them with the
// installed state. Untracked entries are skipped.
func Check(ctx context.Context, f Fetcher, lf *lock.File) []Status {
	type key struct{ src, ref string }
	checkouts := map[key]*source.Checkout{}
	fetchErrs := map[key]error{}

	var out []Status
	for _, e := range lf.Skills {
		if e.Tracking == lock.TrackNone || e.Tracking == "" {
			continue
		}
		st := Status{Entry: e}
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

func compare(st *Status, co *source.Checkout) {
	e := st.Entry
	dir := filepath.Join(co.Dir, filepath.FromSlash(e.Path))
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

// Apply reinstalls the skill of an available update with its recorded agents
// and tracking mode. The lockfile is not saved.
func Apply(in *install.Installer, st Status) (lock.Entry, error) {
	if !st.Available || st.checkout == nil {
		return lock.Entry{}, fmt.Errorf("no update available for %s", st.Entry.Name)
	}
	agents, err := in.Registry.Lookup(st.Entry.Agents)
	if err != nil {
		return lock.Entry{}, err
	}
	s := st.skill
	// Keep the installed directory name even if upstream renamed the skill.
	s.Name = st.Entry.Name
	return in.Install(install.Request{
		Skill:    s,
		Checkout: st.checkout,
		Agents:   agents,
		Tracking: st.Entry.Tracking,
		Force:    true,
	})
}
