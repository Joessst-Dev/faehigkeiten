package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// Retracker switches installed skills to other tracking modes. Unlike
// install.Installer.Retracked, it also switches a skill whose installed copy
// declares no version to version tracking, when upstream declares one. Each
// source is fetched at most once.
type Retracker struct {
	app       *App
	in        *install.Installer
	checkouts map[sourceRef]fetched
}

type sourceRef struct{ src, ref string }

type fetched struct {
	co  *source.Checkout
	err error
}

// NewRetracker returns a retracker for the skills of in.
func (a *App) NewRetracker(in *install.Installer) *Retracker {
	return &Retracker{app: a, in: in, checkouts: map[sourceRef]fetched{}}
}

// Retracked returns e as it is recorded after switching it to tracking mode
// t, or why it cannot switch. A skill without a version that upstream now
// versions records install.UnknownVersion, like adopting such a copy, so any
// upstream version is offered as an update. The lockfile is not changed.
func (r *Retracker) Retracked(ctx context.Context, e lock.Entry, t lock.Tracking) (lock.Entry, error) {
	next, err := r.in.Retracked(e, t)
	if !errors.Is(err, install.ErrNoVersion) {
		return next, err
	}
	up, upErr := r.upstream(ctx, e)
	if upErr != nil {
		return lock.Entry{}, fmt.Errorf("%w (checking upstream: %v)", err, upErr)
	}
	if !up.HasVersion() {
		return lock.Entry{}, fmt.Errorf("skill %s has no version, installed or upstream; use hash tracking instead", e.Name)
	}
	e.Version = install.UnknownVersion
	return r.in.Retracked(e, t)
}

// upstream reads the SKILL.md of e at the tip of its source.
func (r *Retracker) upstream(ctx context.Context, e lock.Entry) (skill.Skill, error) {
	k := sourceRef{e.Source, e.Ref}
	f, ok := r.checkouts[k]
	if !ok {
		f.co, f.err = r.app.Sources.Fetch(ctx, e.Source, e.Ref, true)
		r.checkouts[k] = f
	}
	if f.err != nil {
		return skill.Skill{}, f.err
	}
	dir, err := f.co.SkillDir(e.Path)
	if err != nil {
		return skill.Skill{}, err
	}
	return skill.ParseFile(filepath.Join(dir, skill.FileName))
}
