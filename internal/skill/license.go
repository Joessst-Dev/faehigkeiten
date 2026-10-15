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

// LoadDir reads the skill in dir and resolves its license against root.
func LoadDir(dir, root string) (Skill, error) {
	s, err := ParseFile(filepath.Join(dir, FileName))
	if err != nil {
		return Skill{}, err
	}
	s.Dir = dir
	s.ResolveLicense(root)
	return s, nil
}

// ResolveLicense determines the license of the skill in s.Dir: the license
// field of its frontmatter, a license file in the skill directory, or one in
// root (the repository the skill was found in).
func (s *Skill) ResolveLicense(root string) {
	s.resolveLicense(root, licenseFinder{})
}

func (s *Skill) resolveLicense(root string, lf licenseFinder) {
	s.License = lf.detect(s.Dir, root, s.License.ID)
}

// foundLicense is a license file and the license identified in it.
type foundLicense struct {
	file, id string
}

// licenseFinder caches the license file of each directory it looked at, so
// the license of a repository is read once for all of its skills.
type licenseFinder map[string]foundLicense

func (lf licenseFinder) find(dir string) foundLicense {
	if f, ok := lf[dir]; ok {
		return f
	}
	f := identifyFile(findLicenseFile(dir))
	lf[dir] = f
	return f
}

func identifyFile(file string) foundLicense {
	f := foundLicense{file: file}
	if file != "" {
		if data, err := readLimited(file); err == nil {
			f.id = Identify(string(data))
		}
	}
	return f
}

// detect determines a license from the frontmatter license field fm
// and the license files in dir and root. A field naming a license wins, with
// the first license file that does not contradict it; otherwise the first
// license file found is identified, so fields like "Complete terms in
// LICENSE.txt" resolve to the license of that file.
func (lf licenseFinder) detect(dir, root, fm string) License {
	fm = strings.TrimSpace(Clean(fm))
	if root == "" {
		root = dir
	}
	var candidates []foundLicense
	named := namedFile(dir, fm)
	if named == "" && filepath.Clean(root) != filepath.Clean(dir) {
		named = namedFile(root, fm)
	}
	if named != "" {
		candidates = append(candidates, identifyFile(named))
	}
	candidates = append(candidates, lf.find(dir))
	if filepath.Clean(root) != filepath.Clean(dir) {
		candidates = append(candidates, lf.find(root))
	}

	var l License
	use := func(f foundLicense) {
		if rel, err := filepath.Rel(root, f.file); err == nil && filepath.IsLocal(rel) {
			l.File = filepath.ToSlash(rel)
		}
	}
	if id, ok := spdxID(fm); ok && named == "" && !IsLicenseFile(fm) {
		l.ID = id
		for _, f := range candidates {
			// A file with another well-known license is not the declared one.
			if f.file != "" && (f.id == "" || mentions(id, f.id)) {
				use(f)
				break
			}
		}
		return l
	}
	for _, f := range candidates {
		if f.file != "" {
			use(f)
			l.ID = f.id
			break
		}
	}
	if l.ID == "" {
		l.ID = fm
	}
	return l
}

// namedFile returns the file in dir that the license field fm names, as in
// "license: LICENSE-MIT", or "" when there is none.
func namedFile(dir, fm string) string {
	if dir == "" || fm == "" || !filepath.IsLocal(fm) || strings.ContainsAny(fm, `/\ `) {
		return ""
	}
	p := filepath.Join(dir, fm)
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return p
}

// mentions reports whether the SPDX expression expr includes the license
// id, ignoring suffixes such as "-only" and "-or-later".
func mentions(expr, id string) bool {
	for _, f := range strings.Fields(strings.NewReplacer("(", " ", ")", " ").Replace(expr)) {
		if strings.HasPrefix(strings.ToLower(f), strings.ToLower(id)) {
			return true
		}
	}
	return false
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
	// identifiers alternating with operators, starting and ending with an
	// identifier.
	fields := strings.Fields(strings.NewReplacer("(", " ( ", ")", " ) ").Replace(fm))
	var out []string
	n := 0 // identifiers and operators, without parentheses
	for _, f := range fields {
		if f == "(" || f == ")" {
			out = append(out, f)
			continue
		}
		op := strings.ToUpper(f)
		isOp := op == "OR" || op == "AND" || op == "WITH"
		if n%2 == 1 != isOp {
			return "", false
		}
		n++
		switch {
		case isOp:
			out = append(out, op)
		case !spdxLike.MatchString(f):
			return "", false
		default:
			out = append(out, canonicalID(f))
		}
	}
	if n%2 == 0 {
		return "", false
	}
	if n > 1 {
		// Words such as "MIT or later" are not an expression: every operand
		// must look like a license identifier.
		for i := 0; i < len(out); i++ {
			if f := out[i]; f != "(" && f != ")" && f != "OR" && f != "AND" && f != "WITH" && !idLike(f) {
				return "", false
			}
		}
	}
	return strings.NewReplacer("( ", "(", " )", ")").Replace(strings.Join(out, " ")), true
}

func canonicalID(f string) string {
	if id, ok := knownIDs[strings.ToLower(f)]; ok {
		return id
	}
	return f
}

// idLike reports whether f looks like an SPDX identifier: a known one, a
// LicenseRef or exception, or a name with a version.
func idLike(f string) bool {
	_, known := knownIDs[strings.ToLower(f)]
	return known || strings.HasPrefix(f, "LicenseRef-") || strings.ContainsAny(f, "0123456789")
}

type licenseRule struct {
	id  string
	all []string
	not []string
}

// licenseRules identify license texts by phrases they contain. The first
// phrase locates a license in the text; among rules locating one at the same
// place, the more specific comes first. Texts are compared lower case with
// collapsed whitespace.
var licenseRules = []licenseRule{
	{id: "Unlicense", all: []string{"this is free and unencumbered software released into the public domain"}},
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
	// The license that starts first is the file's own; later ones are
	// usually notices of bundled third-party code.
	id, at := identifyGNU(text)
	for _, r := range licenseRules {
		i := strings.Index(text, r.all[0])
		if i >= 0 && (id == "" || i < at) && containsAll(text, r.all) && !containsAny(text, r.not) {
			id, at = r.id, i
		}
	}
	return id
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

// gnuLicenses are the titles of the GNU licenses and the SPDX identifier of
// each version.
var gnuLicenses = []struct {
	title    string
	versions map[string]string
}{
	{"gnu affero general public license", map[string]string{"3": "AGPL-3.0"}},
	{"gnu lesser general public license", map[string]string{"3": "LGPL-3.0", "2.1": "LGPL-2.1"}},
	{"gnu general public license", map[string]string{"3": "GPL-3.0", "2": "GPL-2.0"}},
}

var gnuVersion = regexp.MustCompile(`version (\d(?:\.\d)?)`)

// identifyGNU identifies a GNU license by the title that comes first, and
// returns where it starts: each of them mentions the others further down, as
// the GPL does the AGPL.
func identifyGNU(text string) (string, int) {
	best, at := -1, len(text)
	for i, l := range gnuLicenses {
		if j := strings.Index(text, l.title); j >= 0 && j < at {
			best, at = i, j
		}
	}
	if best < 0 {
		return "", -1
	}
	// The version follows the title, also in notices such as "the GNU
	// General Public License as published by the Free Software Foundation,
	// either version 3".
	window := text[at:min(len(text), at+160)]
	if m := gnuVersion.FindStringSubmatch(window); m != nil {
		if id := gnuLicenses[best].versions[m[1]]; id != "" {
			return id, at
		}
	}
	return "", -1
}
