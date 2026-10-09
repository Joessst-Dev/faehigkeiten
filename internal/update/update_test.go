package update_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"

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
		sts := update.Check(ctx, mgr, in)
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

		sts := update.Check(ctx, mgr, in)
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

		for _, s := range update.Check(ctx, mgr, in) {
			Expect(s.Available).To(BeFalse())
		}
	})

	It("does not replace directories outside the project through a symlink", func() {
		if runtime.GOOS == "windows" {
			Skip("symlinks need privileges on Windows")
		}
		// A tampered lockfile without a hash bypasses the local-changes check;
		// the symlinked skills directory must still stop the update.
		victim := GinkgoT().TempDir()
		Expect(testutil.WriteFiles(victim, map[string]string{"important/data.txt": "keep"})).To(Succeed())
		Expect(os.RemoveAll(filepath.Join(project, ".claude", "skills"))).To(Succeed())
		Expect(os.Symlink(victim, filepath.Join(project, ".claude", "skills"))).To(Succeed())
		lf.Upsert(lock.Entry{Name: "important", Source: url, Path: "skills/hash", Agents: []string{"claude-code"}, Tracking: lock.TrackHash})

		st := statusFor(update.Check(ctx, mgr, in), "important")
		Expect(st.Available).To(BeTrue())
		_, err := update.Apply(in, st)
		Expect(err).To(MatchError(ContainSubstring("symlink")))
		Expect(filepath.Join(victim, "important", "data.txt")).To(BeAnExistingFile())
	})

	It("reports skills removed upstream", func() {
		wt, _ := repo.Worktree()
		_, err := wt.Remove("skills/ver/SKILL.md")
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.Commit(repo, origin, nil, "remove")).To(Succeed())
		Expect(statusFor(update.Check(ctx, mgr, in), "ver").Err).To(HaveOccurred())
	})

	It("refuses to apply when no update is available", func() {
		_, err := update.Apply(in, statusFor(update.Check(ctx, mgr, in), "ver"))
		Expect(err).To(HaveOccurred())
	})

	bump := func() {
		Expect(testutil.Commit(repo, origin, map[string]string{
			"skills/ver/SKILL.md": testutil.SkillMD("ver", "versioned", "1.1.0"),
		}, "bump")).To(Succeed())
	}

	It("flags locally modified skills", func() {
		installed := filepath.Join(project, ".claude", "skills", "ver", "SKILL.md")
		Expect(os.WriteFile(installed, []byte(testutil.SkillMD("ver", "edited", "1.0.0")), 0o644)).To(Succeed())
		st := statusFor(update.Check(ctx, mgr, in), "ver")
		Expect(st.Modified).To(BeTrue())
		Expect(st.Available).To(BeFalse())

		bump()
		st = statusFor(update.Check(ctx, mgr, in), "ver")
		Expect(st.Modified).To(BeTrue())
		Expect(st.Available).To(BeTrue())
		Expect(statusFor(update.Check(ctx, mgr, in), "hash").Modified).To(BeFalse())
	})

	It("keeps agents that are unknown on this machine", func() {
		e, _ := lf.Get("ver")
		e.Agents = append(e.Agents, "teammates-agent")
		lf.Upsert(e)
		bump()
		updated, err := update.Apply(in, statusFor(update.Check(ctx, mgr, in), "ver"))
		Expect(err).NotTo(HaveOccurred())
		Expect(updated.Agents).To(Equal([]string{"claude-code", "teammates-agent"}))
		Expect(updated.Version).To(Equal("1.1.0"))
		stored, _ := lf.Get("ver")
		Expect(stored.Agents).To(ContainElement("teammates-agent"))
	})

	It("fails clearly when none of the agents are known", func() {
		e, _ := lf.Get("ver")
		e.Agents = []string{"teammates-agent"}
		lf.Upsert(e)
		bump()
		_, err := update.Apply(in, statusFor(update.Check(ctx, mgr, in), "ver"))
		Expect(err).To(MatchError(ContainSubstring("known on this machine")))
	})

	It("keeps the directory name of an adopted skill", func() {
		Expect(in.Uninstall("ver")).To(Succeed())
		Expect(testutil.WriteFiles(project, map[string]string{
			".claude/skills/My Ver/SKILL.md": testutil.SkillMD("ver", "versioned", "1.0.0"),
		})).To(Succeed())
		found := in.Unmanaged()
		Expect(found).To(HaveLen(1))
		co, err := mgr.Fetch(ctx, url, "", false)
		Expect(err).NotTo(HaveOccurred())
		skills, _ := skill.Discover(co.Dir)
		var up skill.Skill
		for _, sk := range skills {
			if sk.Name == "ver" {
				up = sk
			}
		}
		_, err = in.Adopt(install.AdoptRequest{Found: found[0], Checkout: co, Upstream: &up, Tracking: lock.TrackVersion})
		Expect(err).NotTo(HaveOccurred())

		bump()
		_, err = update.Apply(in, statusFor(update.Check(ctx, mgr, in), "My Ver"))
		Expect(err).NotTo(HaveOccurred())
		names := []string{}
		for _, e := range lf.Skills {
			names = append(names, e.Name)
		}
		Expect(names).To(ConsistOf("My Ver", "hash", "loose"))
		Expect(filepath.Join(project, ".claude", "skills", "ver")).NotTo(BeAnExistingFile())
		data, _ := os.ReadFile(filepath.Join(project, ".claude", "skills", "My Ver", "SKILL.md"))
		Expect(string(data)).To(ContainSubstring("1.1.0"))
	})
})
