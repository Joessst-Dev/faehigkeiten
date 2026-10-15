package skill

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// License describes the license of a skill.
type License struct {
	// ID is an SPDX identifier, or the text of the license field of the
	// frontmatter when it is not one and no license file could be identified.
	ID string
	// File is the license file, slash-separated and relative to the root the
	// license was resolved against ("" when there is none).
	File string
}

// Label returns a short description of the license: its ID (only the first
// sentence of a descriptive license field), "custom" for an unidentified
// license file, or "" when no license is known.
func (l License) Label() string {
	switch {
	case l.ID != "":
		if first, _, ok := strings.Cut(l.ID, ". "); ok {
			return first
		}
		return l.ID
	case l.File != "":
		return "custom"
	}
	return ""
}

// maxLicenseSize caps how much of a license file is read.
const maxLicenseSize = 256 << 10

// licenseFiles are the recognised license file names (lower case), in order of
// preference.
var licenseFiles = []string{
	"license", "license.txt", "license.md",
	"licence", "licence.txt", "licence.md",
	"copying", "copying.txt", "copying.md",
}

// IsLicenseFile reports whether name is a recognised license file name.
func IsLicenseFile(name string) bool {
	for _, n := range licenseFiles {
		if strings.EqualFold(name, n) {
			return true
		}
	}
	return false
}

// ResolveLicense determines the license of the skill in s.Dir: the license
// field of its frontmatter, a license file in the skill directory, or one in
// root (the repository the skill was found in).
func (s *Skill) ResolveLicense(root string) {
	s.License = DetectLicense(s.Dir, root, s.License.ID)
}

// DetectLicense determines a license from the frontmatter license field fm
// and the first license file found in dir or, failing that, in root. A short
// SPDX-like field wins; otherwise the license file is identified, so fields
// like "Complete terms in LICENSE.txt" resolve to the license of that file.
func DetectLicense(dir, root, fm string) License {
	fm = strings.TrimSpace(Clean(fm))
	var l License
	file := findLicenseFile(dir)
	if file == "" && root != "" && filepath.Clean(root) != filepath.Clean(dir) {
		file = findLicenseFile(root)
	}
	if file != "" {
		if root == "" {
			root = dir
		}
		if rel, err := filepath.Rel(root, file); err == nil && filepath.IsLocal(rel) {
			l.File = filepath.ToSlash(rel)
		}
	}
	if id, ok := spdxID(fm); ok {
		l.ID = id
		return l
	}
	if file != "" {
		if data, err := readLimited(file); err == nil {
			l.ID = Identify(string(data))
		}
	}
	if l.ID == "" {
		l.ID = fm
	}
	return l
}

func findLicenseFile(dir string) string {
	if dir == "" {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, want := range licenseFiles {
		for _, e := range entries {
			// Symlinks in untrusted repositories could point anywhere on disk.
			if e.Type().IsRegular() && strings.EqualFold(e.Name(), want) {
				return filepath.Join(dir, e.Name())
			}
		}
	}
	return ""
}

// ReadLicenseText reads a license file and makes it safe to print: control
// characters other than newlines are replaced, as by Clean.
func ReadLicenseText(file string) (string, error) {
	data, err := readLimited(file)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), maxLicenseSize+1)
	for sc.Scan() {
		line := strings.ReplaceAll(strings.TrimRight(sc.Text(), "\r"), "\t", "    ")
		b.WriteString(strings.TrimRight(Clean(line), " "))
		b.WriteByte('\n')
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	return strings.TrimRight(b.String(), "\n") + "\n", nil
}

func readLimited(file string) ([]byte, error) {
	fi, err := os.Lstat(file)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", file)
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxLicenseSize))
}

// knownIDs are the SPDX identifiers Identify can recognise, keyed by their
// lower-case form and common aliases.
var knownIDs = map[string]string{}

func init() {
	for _, id := range []string{
		"MIT", "Apache-2.0", "BSD-2-Clause", "BSD-3-Clause", "ISC", "MPL-2.0",
		"GPL-2.0", "GPL-3.0", "LGPL-2.1", "LGPL-3.0", "AGPL-3.0",
		"GPL-2.0-only", "GPL-2.0-or-later", "GPL-3.0-only", "GPL-3.0-or-later",
		"LGPL-2.1-only", "LGPL-2.1-or-later", "LGPL-3.0-only", "LGPL-3.0-or-later",
		"AGPL-3.0-only", "AGPL-3.0-or-later",
		"Unlicense", "CC0-1.0", "CC-BY-4.0", "CC-BY-SA-4.0", "0BSD",
	} {
		knownIDs[strings.ToLower(id)] = id
	}
	for alias, id := range map[string]string{
		"apache-2": "Apache-2.0", "apache2": "Apache-2.0", "apache-2.0-license": "Apache-2.0",
		"apache-license-2.0": "Apache-2.0", "mit-license": "MIT",
		"gplv2": "GPL-2.0", "gplv3": "GPL-3.0", "agplv3": "AGPL-3.0",
	} {
		knownIDs[alias] = id
	}
}

var spdxLike = regexp.MustCompile(`^[A-Za-z0-9.+-]+$`)

// spdxID reports whether fm names a license instead of describing one, and
// returns it with known identifiers in their canonical spelling.
func spdxID(fm string) (string, bool) {
	if fm == "" || len(fm) > 64 {
		return "", false
	}
	if id, ok := knownIDs[strings.ToLower(strings.Join(strings.Fields(fm), "-"))]; ok {
		return id, true
	}
	// Single identifiers or SPDX expressions such as "MIT OR Apache-2.0":
	// identifiers alternating with operators.
	for i, f := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(fm)) {
		op := f == "OR" || f == "AND" || f == "WITH"
		if i%2 == 1 && !op || i%2 == 0 && (op || !spdxLike.MatchString(f)) {
			return "", false
		}
	}
	return fm, true
}

type licenseRule struct {
	id  string
	all []string
	not []string
}

// licenseRules identify license texts by phrases they contain, most specific
// first. Texts are compared lower case with collapsed whitespace.
var licenseRules = []licenseRule{
	{id: "Unlicense", all: []string{"this is free and unencumbered software released into the public domain"}},
	{id: "AGPL-3.0", all: []string{"gnu affero general public license", "version 3"}},
	{id: "LGPL-3.0", all: []string{"gnu lesser general public license", "version 3"}},
	{id: "LGPL-2.1", all: []string{"gnu lesser general public license", "version 2.1"}},
	{id: "GPL-3.0", all: []string{"gnu general public license", "version 3"}},
	{id: "GPL-2.0", all: []string{"gnu general public license", "version 2"}},
	{id: "Apache-2.0", all: []string{"apache license", "version 2.0"}},
	{id: "MPL-2.0", all: []string{"mozilla public license", "2.0"}},
	{id: "CC0-1.0", all: []string{"cc0 1.0"}},
	{id: "CC-BY-SA-4.0", all: []string{"attribution-sharealike 4.0 international"}},
	{id: "CC-BY-4.0", all: []string{"attribution 4.0 international"}},
	{id: "MIT", all: []string{"permission is hereby granted, free of charge", "the above copyright notice and this permission notice shall be included"}},
	{id: "ISC", all: []string{"permission to use, copy, modify, and", "distribute this software for any purpose with or without fee is hereby granted", "the above copyright notice and this permission notice appear in all copies"}},
	{id: "0BSD", all: []string{"permission to use, copy, modify, and", "distribute this software for any purpose with or without fee is hereby granted"}},
	{id: "BSD-3-Clause", all: []string{"redistribution and use in source and binary forms", "neither the name"}},
	{id: "BSD-2-Clause", all: []string{"redistribution and use in source and binary forms"}, not: []string{"neither the name"}},
}

// Identify returns the SPDX identifier of a license text, or "" when it is
// not one of the well-known licenses.
func Identify(text string) string {
	text = strings.ToLower(strings.Join(strings.Fields(text), " "))
	for _, r := range licenseRules {
		if containsAll(text, r.all) && !containsAny(text, r.not) {
			return r.id
		}
	}
	return ""
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
