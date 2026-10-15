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
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
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
	return LicenseText(f.Dirs[0], f.License)
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
// repository's root license, from the cached source.
func (a *App) EntryLicenseText(ctx context.Context, in *install.Installer, e lock.Entry) (string, error) {
	dir, err := in.InstalledDir(e)
	if err != nil {
		return "", err
	}
	l := install.License(e)
	if l == (skill.License{}) {
		// Recorded without a license, e.g. by an older version: look at the
		// installed copy, then at the repository it came from.
		s, err := skill.ParseFile(filepath.Join(dir, skill.FileName))
		if err != nil {
			return "", err
		}
		s.Dir = dir
		s.ResolveLicense(dir)
		if s.License.Label() != "" || e.Source == "" {
			return LicenseText(dir, s.License)
		}
		return a.sourceLicenseText(ctx, e, nil)
	}
	if l.File == "" || e.Source == "" && !inSkill(e.Path, l.File) {
		return LicenseText(dir, skill.License{ID: l.ID})
	}
	if inSkill(e.Path, l.File) {
		rel, _ := strings.CutPrefix(l.File, path.Clean(e.Path)+"/")
		return LicenseText(dir, skill.License{File: rel})
	}
	return a.sourceLicenseText(ctx, e, &l)
}

func inSkill(skillPath, file string) bool {
	p := path.Clean(skillPath)
	return p == "." || strings.HasPrefix(file, p+"/")
}

// sourceLicenseText reads license l from the source of e, or resolves the
// license of the skill there when l is nil. It prefers the installed commit
// and falls back to the latest cached checkout when that commit cannot be
// fetched or lacks the file.
func (a *App) sourceLicenseText(ctx context.Context, e lock.Entry, l *skill.License) (string, error) {
	read := func(co *source.Checkout) (string, error) {
		if l != nil {
			return LicenseText(co.Dir, *l)
		}
		dir, err := co.SkillDir(e.Path)
		if err != nil {
			return "", err
		}
		s, err := skill.ParseFile(filepath.Join(dir, skill.FileName))
		if err != nil {
			return "", err
		}
		s.Dir = dir
		s.ResolveLicense(co.Dir)
		return LicenseText(co.Dir, s.License)
	}
	var firstErr error
	if e.Commit != "" {
		co, err := a.Sources.FetchCommit(ctx, e.Source, e.Ref, e.Commit, e.Path)
		if err == nil {
			text, err := read(co)
			if err == nil || !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, ErrNoLicense) {
				return text, err
			}
		}
		firstErr = err
	}
	co, err := a.Sources.Fetch(ctx, e.Source, e.Ref, false)
	if err != nil {
		return "", errors.Join(firstErr, err)
	}
	return read(co)
}
