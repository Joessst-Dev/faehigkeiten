package app_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
)

var _ = Describe("ProjectRoot", func() {
	It("finds the enclosing git root", func() {
		root := GinkgoT().TempDir()
		sub := filepath.Join(root, "a", "b")
		Expect(os.MkdirAll(sub, 0o755)).To(Succeed())
		Expect(os.Mkdir(filepath.Join(root, ".git"), 0o755)).To(Succeed())
		Expect(app.ProjectRoot(sub)).To(Equal(root))
	})

	It("falls back to the directory itself", func() {
		dir := GinkgoT().TempDir()
		got, err := app.ProjectRoot(dir)
		Expect(err).NotTo(HaveOccurred())
		// Temp dirs are not inside a repository in CI; accept either result if they are.
		Expect(dir).To(HavePrefix(got))
	})

	It("expands only the current user's home", func() {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv("USERPROFILE", home)
		Expect(os.Mkdir(filepath.Join(home, "proj"), 0o755)).To(Succeed())
		Expect(app.ProjectRoot("~/proj")).To(Equal(filepath.Join(home, "proj")))
		_, err := app.ProjectRoot("~proj")
		Expect(err).To(HaveOccurred(), "~proj is a relative path that does not exist")
	})

	It("rejects missing directories and files", func() {
		_, err := app.ProjectRoot(filepath.Join(GinkgoT().TempDir(), "missing"))
		Expect(err).To(HaveOccurred())
		f := filepath.Join(GinkgoT().TempDir(), "file")
		Expect(os.WriteFile(f, nil, 0o644)).To(Succeed())
		_, err = app.ProjectRoot(f)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Select", func() {
	skills := []skill.Skill{{Name: "pdf", Path: "skills/pdf"}, {Name: "docx", Path: "skills/docx"}}

	It("selects by name (case-insensitive) or path", func() {
		got, err := app.Select(skills, []string{"PDF", "skills/docx/"})
		Expect(err).NotTo(HaveOccurred())
		Expect(got).To(HaveLen(2))
	})

	It("fails on unknown skills", func() {
		_, err := app.Select(skills, []string{"xlsx"})
		Expect(err).To(MatchError(ContainSubstring("xlsx")))
	})
})
