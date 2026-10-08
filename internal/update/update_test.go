package update_test

import (
	"context"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
	"github.com/Joessst-Dev/faehigkeiten/internal/update"
)

var _ = Describe("Newer", func() {
	DescribeTable("compares versions",
		func(latest, current string, want bool) {
			Expect(update.Newer(latest, current)).To(Equal(want))
		},
		Entry("semver bump", "1.2.0", "1.1.9", true),
		Entry("semver equal", "v1.0.0", "1.0.0", false),
		Entry("semver older", "1.0.0", "2.0.0", false),
		Entry("numeric", "10", "9", true),
		Entry("non-semver differs", "2024-b", "2024-a", true),
		Entry("non-semver equal", "beta", "beta", false),
	)
})

var _ = Describe("Check and Apply", func() {
	var (
		origin, project string
		repo            *git.Repository
		mgr             *source.Manager
		lf              *lock.File
		in              *install.Installer
		ctx             context.Context
		url             string
	)

	installSkill := func(name string, tracking lock.Tracking) {
		co, err := mgr.Fetch(ctx, url, "", false)
		Expect(err).NotTo(HaveOccurred())
		skills, err := skill.Discover(co.Dir)
		Expect(err).NotTo(HaveOccurred())
		for _, s := range skills {
			if s.Name == name {
				claude, _ := in.Registry.Get("claude-code")
				_, err := in.Install(install.Request{Skill: s, Checkout: co, Agents: []agent.Agent{claude}, Tracking: tracking})
				Expect(err).NotTo(HaveOccurred())
				return
			}
		}
		Fail("missing skill " + name)
	}

	statusFor := func(sts []update.Status, name string) update.Status {
		for _, s := range sts {
			if s.Entry.Name == name {
				return s
			}
		}
		Fail("no status for " + name)
		return update.Status{}
	}

	BeforeEach(func() {
		ctx = context.Background()
		origin, project = GinkgoT().TempDir(), GinkgoT().TempDir()
		var err error
		repo, err = testutil.InitRepo(origin, map[string]string{
			"skills/ver/SKILL.md":   testutil.SkillMD("ver", "versioned", "1.0.0"),
			"skills/hash/SKILL.md":  testutil.SkillMD("hash", "hashed", ""),
			"skills/loose/SKILL.md": testutil.SkillMD("loose", "untracked", ""),
		})
		Expect(err).NotTo(HaveOccurred())
		url = testutil.FileURL(origin)
		mgr = source.NewManager(GinkgoT().TempDir())
		lf, err = lock.Load(filepath.Join(project, lock.FileName))
		Expect(err).NotTo(HaveOccurred())
		in = &install.Installer{Target: install.Target{Scope: agent.ScopeProject, ProjectRoot: project}, Lock: lf, Registry: agent.NewRegistry(nil)}

		installSkill("ver", lock.TrackVersion)
		installSkill("hash", lock.TrackHash)
		installSkill("loose", lock.TrackNone)
	})

	It("reports nothing when upstream is unchanged and skips untracked skills", func() {
		sts := update.Check(ctx, mgr, lf)
		Expect(sts).To(HaveLen(2))
		for _, s := range sts {
			Expect(s.Err).NotTo(HaveOccurred())
			Expect(s.Available).To(BeFalse())
		}
	})

	It("detects version bumps and content changes and applies them", func() {
		Expect(testutil.Commit(repo, origin, map[string]string{
			"skills/ver/SKILL.md":   testutil.SkillMD("ver", "versioned", "1.1.0"),
			"skills/hash/extra.md":  "new file",
			"skills/loose/SKILL.md": testutil.SkillMD("loose", "changed", ""),
		}, "bump")).To(Succeed())

		sts := update.Check(ctx, mgr, lf)
		ver, hash := statusFor(sts, "ver"), statusFor(sts, "hash")
		Expect(ver.Available).To(BeTrue())
		Expect(ver.Current).To(Equal("1.0.0"))
		Expect(ver.Latest).To(Equal("1.1.0"))
		Expect(hash.Available).To(BeTrue())

		for _, s := range []update.Status{ver, hash} {
			_, err := update.Apply(in, s)
			Expect(err).NotTo(HaveOccurred())
		}
		e, _ := lf.Get("ver")
		Expect(e.Version).To(Equal("1.1.0"))
		Expect(filepath.Join(project, ".claude", "skills", "hash", "extra.md")).To(BeAnExistingFile())

		for _, s := range update.Check(ctx, mgr, lf) {
			Expect(s.Available).To(BeFalse())
		}
	})

	It("reports skills removed upstream", func() {
		wt, _ := repo.Worktree()
		_, err := wt.Remove("skills/ver/SKILL.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.Commit(repo, origin, nil, "remove")).To(Succeed())
		Expect(statusFor(update.Check(ctx, mgr, lf), "ver").Err).To(HaveOccurred())
	})

	It("refuses to apply when no update is available", func() {
		_, err := update.Apply(in, statusFor(update.Check(ctx, mgr, lf), "ver"))
		Expect(err).To(HaveOccurred())
	})
})
