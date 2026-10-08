// Package lock reads and writes the lockfile that records installed skills
// and how they are tracked for updates.
package lock

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
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
	Name        string    `yaml:"name"`
	Source      string    `yaml:"source"`
	Path        string    `yaml:"path"`
	Ref         string    `yaml:"ref,omitempty"`
	Commit      string    `yaml:"commit,omitempty"`
	Agents      []string  `yaml:"agents"`
	Tracking    Tracking  `yaml:"tracking"`
	Version     string    `yaml:"version,omitempty"`
	Hash        string    `yaml:"hash,omitempty"`
	InstalledAt time.Time `yaml:"installed_at"`
}

// File is the lockfile content.
type File struct {
	Version int     `yaml:"version"`
	Skills  []Entry `yaml:"skills"`

	path string
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
	if f.Version > currentVersion {
		return nil, fmt.Errorf("%s has lockfile version %d; please upgrade faehigkeiten", p, f.Version)
	}
	f.Version = currentVersion
	return f, nil
}

// Path returns where the lockfile is stored.
func (f *File) Path() string { return f.path }

// Save writes the lockfile, sorted by skill name. An empty lockfile is removed.
func (f *File) Save() error {
	if len(f.Skills) == 0 {
		err := os.Remove(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	slices.SortFunc(f.Skills, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	data, err := marshal(f)
	if err != nil {
		return err
	}
	header := []byte("# Managed by faehigkeiten (https://github.com/Joessst-Dev/faehigkeiten). Do not edit by hand.\n")
	if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(f.path, append(header, data...), 0o644)
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
