package skill_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Parse", func() {
	It("reads name, description and top-level version", func() {
		s, err := skill.Parse([]byte("---\nname: pdf\ndescription: Work with PDFs\nversion: 1.2.0\n---\nbody"))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Name).To(Equal("pdf"))
		Expect(s.Description).To(Equal("Work with PDFs"))
		Expect(s.Version).To(Equal("1.2.0"))
		Expect(s.HasVersion()).To(BeTrue())
	})

	It("falls back to metadata.version", func() {
		s, err := skill.Parse([]byte("---\nname: x\nmetadata:\n  author: me\n  version: \"2.0.1\"\n---\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Version).To(Equal("2.0.1"))
	})

	It("stringifies numeric versions", func() {
		s, err := skill.Parse([]byte("---\nname: x\nversion: 3\n---\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.Version).To(Equal("3"))
	})

	It("reports skills without version", func() {
		s, err := skill.Parse([]byte("---\nname: x\n---\n"))
		Expect(err).NotTo(HaveOccurred())
		Expect(s.HasVersion()).To(BeFalse())
	})

	It("fails without frontmatter", func() {
		_, err := skill.Parse([]byte("# just markdown"))
		Expect(err).To(MatchError(skill.ErrNoFrontmatter))
	})
})

var _ = Describe("Discover", func() {
	var root string

	BeforeEach(func() {
		root = GinkgoT().TempDir()
		Expect(testutil.WriteFiles(root, map[string]string{
			"README.md":                   "repo",
			"skills/pdf/SKILL.md":         testutil.SkillMD("pdf", "PDFs", "1.0.0"),
			"skills/pdf/nested/SKILL.md":  testutil.SkillMD("inner", "ignored", ""),
			"skills/docx/SKILL.md":        "# no frontmatter",
			".git/skills/hidden/SKILL.md": testutil.SkillMD("hidden", "", ""),
			"other/deep/web/SKILL.md":     testutil.SkillMD("web", "Web", ""),
			"node_modules/dep/x/SKILL.md": testutil.SkillMD("dep", "", ""),
			"skills/broken/SKILL.md":      "---\nname: [unclosed\n---\n",
		})).To(Succeed())
	})

	It("finds all skill directories sorted by path", func() {
		skills, err := skill.Discover(root)
		Expect(err).NotTo(HaveOccurred())
		var paths []string
		for _, s := range skills {
			paths = append(paths, s.Path)
		}
		Expect(paths).To(Equal([]string{"other/deep/web", "skills/docx", "skills/pdf"}))
		Expect(skills[1].Name).To(Equal("docx"), "name falls back to the directory")
		Expect(skills[2].Version).To(Equal("1.0.0"))
		Expect(skills[2].Dir).To(Equal(filepath.Join(root, "skills", "pdf")))
	})

	It("treats a root SKILL.md as a single skill", func() {
		single := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(single, map[string]string{"SKILL.md": testutil.SkillMD("solo", "", "")})).To(Succeed())
		skills, err := skill.Discover(single)
		Expect(err).NotTo(HaveOccurred())
		Expect(skills).To(HaveLen(1))
		Expect(skills[0].Path).To(Equal("."))
	})
})

var _ = Describe("Hash", func() {
	var a, b string

	BeforeEach(func() {
		a, b = GinkgoT().TempDir(), GinkgoT().TempDir()
		files := map[string]string{"SKILL.md": "x", "scripts/run.sh": "echo hi"}
		Expect(testutil.WriteFiles(a, files)).To(Succeed())
		Expect(testutil.WriteFiles(b, files)).To(Succeed())
	})

	It("is deterministic for identical content", func() {
		ha, err := skill.Hash(a)
		Expect(err).NotTo(HaveOccurred())
		hb, err := skill.Hash(b)
		Expect(err).NotTo(HaveOccurred())
		Expect(ha).To(Equal(hb))
		Expect(ha).To(HavePrefix(skill.HashPrefix))
	})

	It("changes when content changes", func() {
		before, _ := skill.Hash(a)
		Expect(os.WriteFile(filepath.Join(a, "scripts", "run.sh"), []byte("echo bye"), 0o644)).To(Succeed())
		after, _ := skill.Hash(a)
		Expect(after).NotTo(Equal(before))
	})

	It("changes when a file is renamed", func() {
		before, _ := skill.Hash(a)
		Expect(os.Rename(filepath.Join(a, "scripts", "run.sh"), filepath.Join(a, "scripts", "go.sh"))).To(Succeed())
		after, _ := skill.Hash(a)
		Expect(after).NotTo(Equal(before))
	})

	It("ignores .git directories", func() {
		before, _ := skill.Hash(a)
		Expect(testutil.WriteFiles(a, map[string]string{".git/HEAD": "ref"})).To(Succeed())
		after, _ := skill.Hash(a)
		Expect(after).To(Equal(before))
	})
})
