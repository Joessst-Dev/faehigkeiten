package source_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Normalize", func() {
	DescribeTable("remote inputs",
		func(in, want string) {
			Expect(source.Normalize(in)).To(Equal(want))
		},
		Entry("shorthand", "anthropics/skills", "https://github.com/anthropics/skills"),
		Entry("github host", "github.com/anthropics/skills.git", "https://github.com/anthropics/skills"),
		Entry("https", "https://gitlab.com/a/b/", "https://gitlab.com/a/b"),
		Entry("ssh", "git@github.com:a/b.git", "git@github.com:a/b.git"),
		Entry("file url", "file:///tmp/x", "file:///tmp/x"),
	)

	It("resolves local directories to absolute paths", func() {
		dir := GinkgoT().TempDir()
		got, err := source.Normalize(dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(source.IsLocal(got)).To(BeTrue())
	})

	It("rejects garbage", func() {
		_, err := source.Normalize("not a source")
		Expect(err).To(HaveOccurred())
		_, err = source.Normalize("  ")
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("DisplayName",
		func(in, want string) { Expect(source.DisplayName(in)).To(Equal(want)) },
		Entry("https", "https://github.com/anthropics/skills", "anthropics/skills"),
		Entry("ssh", "git@github.com:a/b.git", "a/b"),
	)
})

var _ = Describe("Manager", func() {
	var (
		origin string
		repo   *git.Repository
		m      *source.Manager
		ctx    context.Context
	)

	BeforeEach(func() {
		ctx = context.Background()
		origin = GinkgoT().TempDir()
		var err error
		repo, err = testutil.InitRepo(origin, map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "A", "1.0.0")})
		Expect(err).NotTo(HaveOccurred())
		m = source.NewManager(GinkgoT().TempDir())
	})

	head := func() string {
		h, err := repo.Head()
		Expect(err).NotTo(HaveOccurred())
		return h.Hash().String()
	}

	It("uses local directories in place and records git metadata", func() {
		co, err := m.Fetch(ctx, origin, "", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(co.Dir).To(Equal(origin))
		Expect(co.Commit).To(Equal(head()))
		Expect(co.Ref).To(Equal("master"))
	})

	It("uses plain directories without git", func() {
		dir := GinkgoT().TempDir()
		co, err := m.Fetch(ctx, dir, "", false)
		Expect(err).NotTo(HaveOccurred())
		Expect(co.Commit).To(BeEmpty())
	})

	Context("with a git URL", func() {
		var url string
		BeforeEach(func() { url = testutil.FileURL(origin) })

		It("clones into the cache and reuses the clone", func() {
			co, err := m.Fetch(ctx, url, "", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(co.Dir).To(HavePrefix(m.CacheDir))
			Expect(co.Commit).To(Equal(head()))
			Expect(filepath.Join(co.Dir, "skills", "a", "SKILL.md")).To(BeAnExistingFile())

			Expect(testutil.Commit(repo, origin, map[string]string{"skills/b/SKILL.md": "x"}, "add b")).To(Succeed())
			again, err := m.Fetch(ctx, url, "", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Commit).To(Equal(co.Commit), "cached clone is reused without refresh")

			fresh, err := m.Fetch(ctx, url, "", true)
			Expect(err).NotTo(HaveOccurred())
			Expect(fresh.Commit).To(Equal(head()))
			Expect(filepath.Join(fresh.Dir, "skills", "b", "SKILL.md")).To(BeAnExistingFile())
		})

		It("checks out a named branch", func() {
			Expect(os.WriteFile(filepath.Join(origin, ".git", "refs", "heads", "dev"), []byte(head()+"\n"), 0o644)).To(Succeed())
			co, err := m.Fetch(ctx, url, "dev", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(co.Ref).To(Equal("dev"))
		})

		It("reports unknown refs", func() {
			_, err := m.Fetch(ctx, url, "nope", false)
			Expect(err).To(MatchError(ContainSubstring("nope")))
		})
	})
})
