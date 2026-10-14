package source_test

import (
	"context"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Versions", func() {
	var (
		ctx    context.Context
		m      *source.Manager
		origin string
		repo   *git.Repository
		// commits maps a label to the commit made for it.
		commits map[string]string
	)

	commit := func(label string, files map[string]string) {
		Expect(testutil.Commit(repo, origin, files, label)).To(Succeed())
		h, err := repo.Head()
		Expect(err).NotTo(HaveOccurred())
		commits[label] = h.Hash().String()
	}

	BeforeEach(func() {
		ctx = context.Background()
		m = source.NewManager(GinkgoT().TempDir())
		origin = GinkgoT().TempDir()
		commits = map[string]string{}
		var err error
		repo, err = testutil.InitRepo(origin, map[string]string{"README.md": "x"})
		Expect(err).NotTo(HaveOccurred())
		commit("1.0.0", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "1.0.0")})
		commit("fix", map[string]string{"skills/a/run.sh": "echo fixed"})
		commit("other", map[string]string{"skills/b/SKILL.md": testutil.SkillMD("b", "d", "9.0.0")})
		commit("1.1.0", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "1.1.0")})
		commit("unversioned", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "")})
		commit("2.0.0", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "2.0.0")})
	})

	It("lists every version newest first with its last commit", func() {
		vs, err := m.Versions(ctx, testutil.FileURL(origin), "", "skills/a", true)
		Expect(err).NotTo(HaveOccurred())
		var got []string
		for _, v := range vs {
			got = append(got, v.Version+"@"+v.Commit)
		}
		Expect(got).To(Equal([]string{
			"2.0.0@" + commits["2.0.0"],
			"1.1.0@" + commits["1.1.0"],
			"1.0.0@" + commits["fix"],
		}))
	})

	It("marks the version at the tip as the latest", func() {
		url := testutil.FileURL(origin)
		vs, err := m.Versions(ctx, url, "", "skills/a", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs[0].Latest).To(BeTrue())
		Expect(vs[1].Latest).To(BeFalse())

		commit("dropped", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "")})
		vs, err = m.Versions(ctx, url, "", "skills/a", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs[0].Version).To(Equal("2.0.0"))
		Expect(vs[0].Latest).To(BeFalse(), "the tip no longer declares a version")
	})

	It("sees new versions after an update", func() {
		url := testutil.FileURL(origin)
		_, err := m.Versions(ctx, url, "", "skills/a", true)
		Expect(err).NotTo(HaveOccurred())
		commit("3.0.0", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "3.0.0")})
		vs, err := m.Versions(ctx, url, "", "skills/a", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs[0].Version).To(Equal("3.0.0"))
	})

	It("reads the history of a local repository", func() {
		vs, err := m.Versions(ctx, filepath.Join(origin, "skills"), "", "a", true)
		Expect(err).NotTo(HaveOccurred())
		Expect(vs).To(HaveLen(3))
		Expect(vs[2].Commit).To(Equal(commits["fix"]))
	})

	Describe("FetchCommit", func() {
		It("writes the tree of an older commit and reuses it", func() {
			url := testutil.FileURL(origin)
			co, err := m.FetchCommit(ctx, url, "", commits["fix"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			Expect(co.Commit).To(Equal(commits["fix"]))
			Expect(co.Ref).To(Equal("master"))
			Expect(os.ReadFile(filepath.Join(co.Dir, "skills", "a", "SKILL.md"))).To(ContainSubstring("version: 1.0.0"))
			Expect(filepath.Join(co.Dir, "skills", "a", "run.sh")).To(BeAnExistingFile())
			Expect(filepath.Join(co.Dir, "README.md")).NotTo(BeAnExistingFile(), "only the skill is written")
			Expect(filepath.Join(co.Dir, "skills", "b")).NotTo(BeAnExistingFile())

			again, err := m.FetchCommit(ctx, url, "", commits["fix"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			Expect(again.Dir).To(Equal(co.Dir))
		})

		It("fetches commits newer than the cached history", func() {
			url := testutil.FileURL(origin)
			_, err := m.FetchCommit(ctx, url, "", commits["1.0.0"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			commit("3.0.0", map[string]string{"skills/a/SKILL.md": testutil.SkillMD("a", "d", "3.0.0")})
			co, err := m.FetchCommit(ctx, url, "", commits["3.0.0"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(co.Dir, "skills", "a", "SKILL.md"))).To(ContainSubstring("version: 3.0.0"))
		})

		It("resolves skills relative to a local directory", func() {
			co, err := m.FetchCommit(ctx, filepath.Join(origin, "skills"), "", commits["1.1.0"], "a")
			Expect(err).NotTo(HaveOccurred())
			Expect(os.ReadFile(filepath.Join(co.Dir, "a", "SKILL.md"))).To(ContainSubstring("version: 1.1.0"))
		})

		It("keeps the versions it has when updating the history fails", func() {
			upstream := GinkgoT().TempDir()
			Expect(os.CopyFS(upstream, os.DirFS(origin))).To(Succeed())
			url := testutil.FileURL(upstream)
			_, err := m.Versions(ctx, url, "", "skills/a", true)
			Expect(err).NotTo(HaveOccurred())
			Expect(os.RemoveAll(upstream)).To(Succeed())
			vs, err := m.Versions(ctx, url, "", "skills/a", true)
			Expect(err).NotTo(HaveOccurred())
			Expect(vs).To(HaveLen(3))
		})

		It("shares the history of the default branch with its name", func() {
			url := testutil.FileURL(origin)
			_, err := m.Versions(ctx, url, "", "skills/a", true)
			Expect(err).NotTo(HaveOccurred())
			_, err = m.FetchCommit(ctx, url, "master", commits["1.1.0"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			histories, _ := filepath.Glob(filepath.Join(m.CacheDir, "repos", "*.history"))
			Expect(histories).To(HaveLen(1))
		})

		It("reuses cached trees without the history", func() {
			upstream := GinkgoT().TempDir()
			Expect(os.CopyFS(upstream, os.DirFS(origin))).To(Succeed())
			url := testutil.FileURL(upstream)
			co, err := m.FetchCommit(ctx, url, "master", commits["fix"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			histories, _ := filepath.Glob(filepath.Join(m.CacheDir, "repos", "*.history"))
			for _, h := range histories {
				Expect(os.RemoveAll(h)).To(Succeed())
			}
			Expect(os.RemoveAll(upstream)).To(Succeed())
			again, err := m.FetchCommit(ctx, url, "master", commits["fix"], "skills/a")
			Expect(err).NotTo(HaveOccurred())
			Expect(again).To(Equal(co))
		})

		It("reads pinned commits of a local repository on another branch", func() {
			wt, err := repo.Worktree()
			Expect(err).NotTo(HaveOccurred())
			Expect(wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName("feature"), Create: true})).To(Succeed())
			co, err := m.FetchCommit(ctx, filepath.Join(origin, "skills"), "master", commits["1.1.0"], "a")
			Expect(err).NotTo(HaveOccurred())
			Expect(co.Ref).To(Equal("master"))
			Expect(os.ReadFile(filepath.Join(co.Dir, "a", "SKILL.md"))).To(ContainSubstring("version: 1.1.0"))
		})

		It("rejects invalid commits", func() {
			_, err := m.FetchCommit(ctx, testutil.FileURL(origin), "", "nope", "skills/a")
			Expect(err).To(MatchError(ContainSubstring("invalid commit")))
		})
	})
})
