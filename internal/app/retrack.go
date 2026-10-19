package app

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// Retracker switches installed skills to other tracking modes, checking
// upstream for the choices that need it (see install.Choice.NeedsUpstream).
// Each source is fetched at most once; it is safe for concurrent use.
type Retracker struct {
	app       *App
	in        *install.Installer
	checkouts memo[*source.Checkout]
}

// NewRetracker returns a retracker for the skills of in.
func (a *App) NewRetracker(in *install.Installer) *Retracker {
	return &Retracker{app: a, in: in}
}

// Retracked returns e as it is recorded after switching it to tracking mode
// t, or why it cannot switch. The lockfile is not changed.
func (r *Retracker) Retracked(ctx context.Context, e lock.Entry, t lock.Tracking) (lock.Entry, error) {
	return r.Confirm(ctx, r.in.Retracked(e, t))
}

// Confirm returns the entry of c, or why it cannot switch. A choice that
// needs upstream is only valid when upstream declares a version: the skill
// then records install.UnknownVersion, like adopting such a copy, so any
// upstream version is offered as an update.
func (r *Retracker) Confirm(ctx context.Context, c install.Choice) (lock.Entry, error) {
	if c.Err != nil || !c.NeedsUpstream {
		return c.Entry, c.Err
	}
	e := c.Entry
	up, err := r.upstream(ctx, e)
	if err != nil {
		return lock.Entry{}, fmt.Errorf("skill %s has no version installed; checking upstream: %w", e.Name, err)
	}
	if !up.HasVersion() {
		return lock.Entry{}, fmt.Errorf("skill %s has no version, installed or upstream; use hash tracking instead", e.Name)
	}
	return e, nil
}

// upstream reads the SKILL.md of e at the tip of its source.
func (r *Retracker) upstream(ctx context.Context, e lock.Entry) (skill.Skill, error) {
	// NUL cannot appear in a URL or ref, so keys cannot collide.
	co, err := r.checkouts.get(ctx, e.Source+"\x00"+e.Ref, func() (*source.Checkout, error) {
		return r.app.Sources.Fetch(ctx, e.Source, e.Ref, true)
	})
	if err != nil {
		return skill.Skill{}, err
	}
	dir, err := co.SkillDir(e.Path)
	if err != nil {
		return skill.Skill{}, err
	}
	return skill.ParseFile(filepath.Join(dir, skill.FileName))
}
