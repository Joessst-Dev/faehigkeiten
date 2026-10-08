// Package skill discovers agent skills (directories containing a SKILL.md)
// and computes their metadata and content hashes.
package skill

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileName is the marker file that identifies a skill directory.
const FileName = "SKILL.md"

// HashPrefix is prepended to every content hash.
const HashPrefix = "sha256:"

// Skill describes a single skill found in a repository tree.
type Skill struct {
	Name        string
	Description string
	Version     string
	// Path is the skill directory relative to the scanned root, using forward slashes.
	Path string
	// Dir is the absolute directory on disk.
	Dir string
}

// HasVersion reports whether the skill declares a version in its frontmatter.
func (s Skill) HasVersion() bool { return s.Version != "" }

type frontmatter struct {
	Name        string         `yaml:"name"`
	Description string         `yaml:"description"`
	Version     any            `yaml:"version"`
	Metadata    map[string]any `yaml:"metadata"`
}

// ErrNoFrontmatter is returned when a SKILL.md has no YAML frontmatter block.
var ErrNoFrontmatter = errors.New("no frontmatter")

// ParseFile reads a SKILL.md and returns its metadata. Name falls back to the
// directory name when the frontmatter omits it.
func ParseFile(file string) (Skill, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Skill{}, err
	}
	s, err := Parse(data)
	if err != nil && !errors.Is(err, ErrNoFrontmatter) {
		return Skill{}, fmt.Errorf("parse %s: %w", file, err)
	}
	if s.Name == "" {
		s.Name = filepath.Base(filepath.Dir(file))
	}
	return s, nil
}

// Parse extracts metadata from SKILL.md content.
func Parse(data []byte) (Skill, error) {
	block, ok := extractFrontmatter(data)
	if !ok {
		return Skill{}, ErrNoFrontmatter
	}
	var fm frontmatter
	if err := yaml.Unmarshal(block, &fm); err != nil {
		return Skill{}, err
	}
	s := Skill{
		Name:        strings.TrimSpace(fm.Name),
		Description: strings.TrimSpace(fm.Description),
		Version:     scalar(fm.Version),
	}
	if s.Version == "" && fm.Metadata != nil {
		s.Version = scalar(fm.Metadata["version"])
	}
	return s, nil
}

func scalar(v any) string {
	if v == nil {
		return ""
	}
	switch v.(type) {
	case map[string]any, []any:
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func extractFrontmatter(data []byte) ([]byte, bool) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "---" {
		return nil, false
	}
	var buf bytes.Buffer
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "---" {
			return buf.Bytes(), true
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	return nil, false
}

var skippedDirs = map[string]bool{".git": true, "node_modules": true}

// Discover walks root and returns every skill directory below it, sorted by path.
// Skills nested inside another skill directory are not reported separately.
func Discover(root string) ([]Skill, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var skills []Skill
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// A symlinked SKILL.md in an untrusted repository could point anywhere
		// on disk, and symlinks are not copied on install anyway.
		if d.Name() != FileName || !d.Type().IsRegular() {
			return nil
		}
		s, err := ParseFile(p)
		if err != nil {
			// A malformed skill should not break discovery of the others.
			return nil
		}
		dir := filepath.Dir(p)
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return err
		}
		s.Dir = dir
		s.Path = filepath.ToSlash(rel)
		skills = append(skills, s)
		if dir != root {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Path < skills[j].Path })
	return skills, nil
}

// Hash returns a deterministic content hash of the skill directory: SHA-256
// over every file's slash-separated relative path and content, in sorted order.
func Hash(dir string) (string, error) {
	// WalkDir does not descend into a symlinked root, and installers commonly
	// link skill directories, so resolve it first.
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	var files []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != dir && skippedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, rel := range files {
		if err := hashFile(h, dir, rel); err != nil {
			return "", err
		}
	}
	return HashPrefix + hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(w io.Writer, dir, rel string) error {
	f, err := os.Open(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Fprintf(w, "%s\x00", path.Clean(rel))
	_, err = io.Copy(w, f)
	fmt.Fprint(w, "\x00")
	return err
}
