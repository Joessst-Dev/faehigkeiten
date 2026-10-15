package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
)

// ErrNoLicense is returned when a skill declares no license and has no
// license file.
var ErrNoLicense = errors.New("no license found")

// LicenseText returns the text of license l resolved against root: its
// license file, or the license field when there is none.
func LicenseText(root string, l skill.License) (string, error) {
	if l.File != "" {
		return skill.ReadLicenseText(filepath.Join(root, filepath.FromSlash(l.File)))
	}
	if l.ID != "" {
		return l.ID + "\n", nil
	}
	return "", ErrNoLicense
}

// FoundLicenseText returns the license text of an unmanaged skill.
func FoundLicenseText(f install.Found) (string, error) {
	if len(f.Dirs) == 0 {
		return "", fmt.Errorf("skill %s has no directories", f.Name)
	}
	return LicenseText(f.Dirs[0], f.License())
}

// SkillLicenseText returns the license text of the skill named name (its
// name, path or directory name) in the branch or tag ref of src.
func (a *App) SkillLicenseText(ctx context.Context, src, ref, name string) (string, error) {
	co, skills, err := a.Load(ctx, src, ref, false)
	if err != nil {
		return "", err
	}
	selected, err := Select(skills, []string{name})
	if err != nil {
		// Directories such as skills.sh lists them are named by the directory.
		i := slices.IndexFunc(skills, func(s skill.Skill) bool { return strings.EqualFold(path.Base(s.Path), name) })
		if i < 0 {
			return "", err
		}
		selected = skills[i : i+1]
	}
	text, err := LicenseText(co.Dir, selected[0].License)
	if err != nil {
		return "", fmt.Errorf("%s: %w", selected[0].Name, err)
	}
	return text, nil
}

// EntryLicenseText returns the license text of an installed skill. License
// files inside the skill are read from the installed copy; others, such as a
// repository's root license, from the source at the installed commit.
func (a *App) EntryLicenseText(ctx context.Context, in *install.Installer, e lock.Entry) (string, error) {
	if e.NoLicense {
		return "", ErrNoLicense
	}
	dir, err := in.InstalledDir(e)
	if err != nil {
		return "", err
	}
	l := install.License(e)
	switch {
	case l == (skill.License{}):
		// Recorded by an older version: look at the installed copy, then at
		// the source it came from.
		s, err := skill.LoadDir(dir, dir)
		if err != nil {
			return "", err
		}
		if s.License.Label() != "" || e.Source == "" {
			return LicenseText(dir, s.License)
		}
		return a.resolveSourceLicense(ctx, e)
	case l.File == "":
		return LicenseText(dir, l)
	case inSkill(e.Path, l.File):
		rel, _ := strings.CutPrefix(l.File, path.Clean(e.Path)+"/")
		text, err := LicenseText(dir, skill.License{File: rel})
		if errors.Is(err, fs.ErrNotExist) && e.Source != "" {
			// Deleted from the installed copy, or adopted from a source with
			// different content.
			return a.sourceLicenseFile(ctx, e, l.File)
		}
		return text, err
	case e.Source == "":
		return LicenseText(dir, skill.License{ID: l.ID})
	}
	return a.sourceLicenseFile(ctx, e, l.File)
}

func inSkill(skillPath, file string) bool {
	p := path.Clean(skillPath)
	return p == "." || strings.HasPrefix(file, p+"/")
}

// sourceLicenseFile reads the recorded license file of e from its source at
// the installed commit. When that commit cannot be fetched, e.g. because the
// history was rewritten or is not cached offline, it reads the file from the
// latest cached checkout instead: the file is the one recorded at install.
func (a *App) sourceLicenseFile(ctx context.Context, e lock.Entry, file string) (string, error) {
	l := skill.License{File: file}
	var commitErr error
	if e.Commit != "" {
		co, err := a.Sources.FetchCommit(ctx, e.Source, e.Ref, e.Commit, e.Path)
		if err == nil {
			return LicenseText(co.Dir, l)
		}
		commitErr = err
	}
	co, err := a.Sources.Fetch(ctx, e.Source, e.Ref, false)
	if err != nil {
		return "", errors.Join(commitErr, err)
	}
	return LicenseText(co.Dir, l)
}

// resolveSourceLicense resolves the license of e, recorded without one, from
// its source at the installed commit. Unlike sourceLicenseFile it does not
// fall back to a later commit, whose license may differ from the installed
// one; only skills installed without a known commit use the latest checkout.
func (a *App) resolveSourceLicense(ctx context.Context, e lock.Entry) (string, error) {
	if e.Commit != "" {
		co, s, err := a.LoadCommit(ctx, e.Source, e.Ref, e.Path, e.Commit)
		if err != nil {
			return "", err
		}
		return LicenseText(co.Dir, s.License)
	}
	co, skills, err := a.Load(ctx, e.Source, e.Ref, false)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(skills, func(s skill.Skill) bool { return s.Path == e.Path })
	if i < 0 {
		return "", fmt.Errorf("skill %s no longer found at %s", e.Name, e.Path)
	}
	return LicenseText(co.Dir, skills[i].License)
}
