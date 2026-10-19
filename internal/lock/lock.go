// Package lock reads and writes the lockfile that records installed skills
// and how they are tracked for updates.
package lock

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
)

// FileName is the lockfile name used for project installations.
const FileName = ".faehigkeiten.lock.yaml"

// GlobalFileName is the lockfile name used for global installations.
const GlobalFileName = "global.lock.yaml"

const currentVersion = 1

// Tracking controls how a skill is checked for updates.
type Tracking string

const (
	// TrackVersion compares the version declared in the skill's frontmatter.
	TrackVersion Tracking = "version"
	// TrackHash compares a content hash of the skill directory.
	TrackHash Tracking = "hash"
	// TrackNone disables update checks.
	TrackNone Tracking = "none"
)

// ParseTracking validates a tracking mode string.
func ParseTracking(s string) (Tracking, error) {
	switch t := Tracking(strings.ToLower(s)); t {
	case TrackVersion, TrackHash, TrackNone:
		return t, nil
	}
	return "", fmt.Errorf("invalid tracking mode %q (want version, hash or none)", s)
}

// Entry describes one installed skill.
type Entry struct {
	Name     string   `yaml:"name"`
	Source   string   `yaml:"source"`
	Path     string   `yaml:"path"`
	Ref      string   `yaml:"ref,omitempty"`
	Commit   string   `yaml:"commit,omitempty"`
	Agents   []string `yaml:"agents"`
	Tracking Tracking `yaml:"tracking"`
	// Version is the installed version. It is recorded for version tracking
	// and kept when a skill is switched to hash tracking.
	Version string `yaml:"version,omitempty"`
	Hash    string `yaml:"hash,omitempty"`
	// Pinned entries were installed at a chosen version and are not updated.
	Pinned bool `yaml:"pinned,omitempty"`
	// License is the ID of the skill's license and LicenseFile its license
	// file relative to the source root (see skill.License).
	License     string `yaml:"license,omitempty"`
	LicenseFile string `yaml:"license_file,omitempty"`
	// NoLicense records that the skill has no license, unlike entries
	// written before licenses were recorded.
	NoLicense   bool      `yaml:"no_license,omitempty"`
	InstalledAt time.Time `yaml:"installed_at"`
}

// File is the lockfile content.
type File struct {
	Version int     `yaml:"version"`
	Skills  []Entry `yaml:"skills"`

	path string
	// exists records that the file is on disk, so saving keeps it even
	// when it holds no skills.
	exists bool
}

// PathFor returns the lockfile location for a scope.
func PathFor(scope agent.Scope, projectRoot, configDir string) (string, error) {
	switch scope {
	case agent.ScopeProject:
		if projectRoot == "" {
			return "", errors.New("project root required")
		}
		return filepath.Join(projectRoot, FileName), nil
	case agent.ScopeGlobal:
		return filepath.Join(configDir, GlobalFileName), nil
	}
	return "", fmt.Errorf("unknown scope %q", scope)
}

// Load reads the lockfile at p; a missing file yields an empty lockfile.
func Load(p string) (*File, error) {
	f := &File{Version: currentVersion, path: p}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	f.exists = true
	if f.Version > currentVersion {
		return nil, fmt.Errorf("%s has lockfile version %d; please upgrade faehigkeiten", p, f.Version)
	}
	f.Version = currentVersion
	for _, e := range f.Skills {
		if err := e.validate(); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}
	return f, nil
}

// ValidateName rejects skill directory names that could escape or hide in an
// agent's skill directory: path separators, drive or stream separators,
// control characters and leading dots (which also covers "." and "..").
func ValidateName(name string) error {
	if name == "" || strings.HasPrefix(name, ".") || strings.ContainsAny(name, `/\:`) ||
		hasControl(name) || !filepath.IsLocal(name) {
		return fmt.Errorf("invalid skill name %q", name)
	}
	return nil
}

// validate rejects entries that could make file operations escape the skill
// directories or garble terminal output, e.g. from a tampered lockfile
// committed to a project.
func (e Entry) validate() error {
	if err := ValidateName(e.Name); err != nil {
		return err
	}
	if e.Path != "" && e.Path != "." && !filepath.IsLocal(filepath.FromSlash(e.Path)) {
		return fmt.Errorf("skill %s: invalid path %q", e.Name, e.Path)
	}
	// License files are read below the skill or source directory, so their
	// path must be canonical: "skills/pdf/../../x" would pass a prefix check.
	if f := e.LicenseFile; f != "" && (path.Clean(f) != f || !filepath.IsLocal(filepath.FromSlash(f))) {
		return fmt.Errorf("skill %s: invalid license file %q", e.Name, f)
	}
	for _, v := range append([]string{e.Source, e.Path, e.Ref, e.Commit, e.Version, e.Hash, string(e.Tracking), e.License, e.LicenseFile}, e.Agents...) {
		if hasControl(v) {
			return fmt.Errorf("skill %s: invalid value %q", e.Name, v)
		}
	}
	return nil
}

func hasControl(s string) bool {
	return skill.Clean(s) != s
}

// Path returns where the lockfile is stored.
func (f *File) Path() string { return f.path }

// Exists reports whether the lockfile is on disk.
func (f *File) Exists() bool { return f.exists }

// Init writes the lockfile even though it holds no skills, so it can be
// committed before anything is installed. It fails with fs.ErrExist when the
// file is on disk, also when it was created after the lockfile was loaded.
func (f *File) Init() error {
	if f.exists {
		return fmt.Errorf("%s: %w", f.path, fs.ErrExist)
	}
	data, err := f.encode()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(f.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = out.Write(data)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	f.exists = true
	return nil
}

// Save writes the lockfile, sorted by skill name. An empty lockfile is only
// written when it is already on disk; once created, the file is kept.
func (f *File) Save() error {
	if len(f.Skills) == 0 && !f.exists {
		return nil
	}
	data, err := f.encode()
	if err != nil {
		return err
	}
	if err := os.WriteFile(f.path, data, 0o644); err != nil {
		return err
	}
	f.exists = true
	return nil
}

// encode sorts the skills by name and returns the file content. It also
// creates the directory of the lockfile.
func (f *File) encode() ([]byte, error) {
	if f.Skills == nil {
		f.Skills = []Entry{} // "skills: []" rather than "skills: null"
	}
	slices.SortFunc(f.Skills, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	data, err := marshal(f)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return nil, err
	}
	header := []byte("# Managed by faehigkeiten (https://github.com/Joessst-Dev/faehigkeiten). Do not edit by hand.\n")
	return append(header, data...), nil
}

// Get returns the entry for the named skill.
func (f *File) Get(name string) (Entry, bool) {
	for _, e := range f.Skills {
		if e.Name == name {
			return e, true
		}
	}
	return Entry{}, false
}

// Upsert inserts or replaces the entry with the same name.
func (f *File) Upsert(e Entry) {
	for i := range f.Skills {
		if f.Skills[i].Name == e.Name {
			f.Skills[i] = e
			return
		}
	}
	f.Skills = append(f.Skills, e)
}

// Remove deletes the named entry and reports whether it existed.
func (f *File) Remove(name string) bool {
	n := len(f.Skills)
	f.Skills = slices.DeleteFunc(f.Skills, func(e Entry) bool { return e.Name == name })
	return len(f.Skills) != n
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
