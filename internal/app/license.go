package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
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
	return LicenseText(f.Dirs[0], f.License)
}

// EntryLicenseText returns the license text of an installed skill. License
// files inside the skill are read from the installed copy; others, such as a
// repository's root license, from the cached source.
func (a *App) EntryLicenseText(ctx context.Context, in *install.Installer, e lock.Entry) (string, error) {
	dir, err := in.InstalledDir(e)
	if err != nil {
		return "", err
	}
	if e.License == "" && e.LicenseFile == "" {
		// Recorded without a license, e.g. by an older version: look at the
		// installed copy.
		s, err := skill.ParseFile(filepath.Join(dir, skill.FileName))
		if err != nil {
			return "", err
		}
		s.Dir = dir
		s.ResolveLicense(dir)
		return LicenseText(dir, s.License)
	}
	if e.LicenseFile == "" {
		return LicenseText(dir, skill.License{ID: e.License})
	}
	if p := path.Clean(e.Path); p == "." || strings.HasPrefix(e.LicenseFile, p+"/") {
		rel := strings.TrimPrefix(e.LicenseFile, p+"/")
		return LicenseText(dir, skill.License{File: rel})
	}
	if e.Source == "" {
		return LicenseText(dir, skill.License{ID: e.License})
	}
	l := skill.License{File: e.LicenseFile}
	if e.Commit != "" {
		co, err := a.Sources.FetchCommit(ctx, e.Source, e.Ref, e.Commit, e.Path)
		if err != nil {
			return "", err
		}
		text, err := LicenseText(co.Dir, l)
		if !errors.Is(err, fs.ErrNotExist) {
			return text, err
		}
		// Commit trees cached by older versions lack the root license files.
	}
	co, err := a.Sources.Fetch(ctx, e.Source, e.Ref, false)
	if err != nil {
		return "", err
	}
	return LicenseText(co.Dir, l)
}
