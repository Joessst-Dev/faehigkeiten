package source_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

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

	DescribeTable("rejects unencrypted URLs",
		func(in string) {
			_, err := source.Normalize(in)
			Expect(err).To(MatchError(ContainSubstring("insecure")))
		},
		Entry("http", "http://example.com/skills.git"),
		Entry("git protocol", "git://example.com/skills.git"),
	)

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

	Describe("SkillDir", func() {
		var co *source.Checkout

		BeforeEach(func() {
			if runtime.GOOS == "windows" {
				Skip("symlinks need privileges on Windows")
			}
			co = &source.Checkout{URL: "https://example.com/a/b", Dir: GinkgoT().TempDir()}
			Expect(testutil.WriteFiles(co.Dir, map[string]string{"skills/x/SKILL.md": "x"})).To(Succeed())
		})

		It("returns directories inside the checkout", func() {
			Expect(co.SkillDir("skills/x")).To(Equal(filepath.Join(co.Dir, "skills", "x")))
			Expect(co.SkillDir(".")).To(Equal(co.Dir))
			Expect(os.Symlink("skills", filepath.Join(co.Dir, "alias"))).To(Succeed())
			Expect(co.SkillDir("alias/x")).To(Equal(filepath.Join(co.Dir, "alias", "x")))
		})

		It("rejects paths that a symlink leads outside the checkout", func() {
			outside := GinkgoT().TempDir()
			Expect(testutil.WriteFiles(outside, map[string]string{"private/SKILL.md": "secret"})).To(Succeed())
			Expect(os.Symlink(outside, filepath.Join(co.Dir, "escape"))).To(Succeed())
			_, err := co.SkillDir("escape/private")
			Expect(err).To(MatchError(ContainSubstring("outside")))
		})
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

		It("updates an existing clone in place", func() {
			co, err := m.Fetch(ctx, url, "", false)
			Expect(err).NotTo(HaveOccurred())
			marker := filepath.Join(co.Dir, ".git", "faehigkeiten-marker")
			Expect(os.WriteFile(marker, nil, 0o644)).To(Succeed())

			wt, _ := repo.Worktree()
			_, err = wt.Remove("skills/a/SKILL.md")
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.Commit(repo, origin, map[string]string{"skills/c/SKILL.md": "c"}, "replace a with c")).To(Succeed())

			fresh, err := m.Fetch(ctx, url, "", true)
			Expect(err).NotTo(HaveOccurred())
			Expect(fresh.Dir).To(Equal(co.Dir))
			Expect(fresh.Commit).To(Equal(head()))
			Expect(marker).To(BeAnExistingFile(), "the clone was updated, not replaced")
			Expect(filepath.Join(fresh.Dir, "skills", "c", "SKILL.md")).To(BeAnExistingFile())
			Expect(filepath.Join(fresh.Dir, "skills", "a")).NotTo(BeAnExistingFile())
			leftovers, _ := filepath.Glob(filepath.Join(m.CacheDir, "repos", ".*tmp-*"))
			Expect(leftovers).To(BeEmpty())
		})

		It("keeps clones of similar URLs apart", func() {
			parent := GinkgoT().TempDir()
			_, err := testutil.InitRepo(filepath.Join(parent, "a", "b"), map[string]string{"x/SKILL.md": "one"})
			Expect(err).NotTo(HaveOccurred())
			_, err = testutil.InitRepo(filepath.Join(parent, "a_b"), map[string]string{"y/SKILL.md": "two"})
			Expect(err).NotTo(HaveOccurred())
			one, err := m.Fetch(ctx, testutil.FileURL(filepath.Join(parent, "a", "b")), "", false)
			Expect(err).NotTo(HaveOccurred())
			two, err := m.Fetch(ctx, testutil.FileURL(filepath.Join(parent, "a_b")), "", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(one.Dir).NotTo(Equal(two.Dir))
			Expect(filepath.Join(two.Dir, "y", "SKILL.md")).To(BeAnExistingFile())
		})

		It("checks out a named branch", func() {
			Expect(os.WriteFile(filepath.Join(origin, ".git", "refs", "heads", "dev"), []byte(head()+"\n"), 0o644)).To(Succeed())
			def, err := m.Fetch(ctx, url, "", false)
			Expect(err).NotTo(HaveOccurred())
			co, err := m.Fetch(ctx, url, "dev", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(co.Ref).To(Equal("dev"))
			Expect(co.Dir).NotTo(Equal(def.Dir), "another branch needs its own clone")
		})

		It("reuses the default-branch clone when its branch is requested by name", func() {
			def, err := m.Fetch(ctx, url, "", false)
			Expect(err).NotTo(HaveOccurred())
			Expect(def.Ref).To(Equal("master"))

			Expect(testutil.Commit(repo, origin, map[string]string{"skills/b/SKILL.md": "b"}, "add b")).To(Succeed())
			named, err := m.Fetch(ctx, url, "master", true)
			Expect(err).NotTo(HaveOccurred())
			Expect(named.Dir).To(Equal(def.Dir))
			Expect(named.Commit).To(Equal(head()))

			clones, _ := os.ReadDir(filepath.Join(m.CacheDir, "repos"))
			Expect(clones).To(HaveLen(1), "only one clone of the repository exists")
		})

		It("reports unknown refs", func() {
			_, err := m.Fetch(ctx, url, "nope", false)
			Expect(err).To(MatchError(ContainSubstring("nope")))
		})
	})
})
