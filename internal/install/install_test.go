package install_test

import (
	"os"
	"path/filepath"
	"runtime"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("Installer", func() {
	var (
		repoDir, project string
		reg              *agent.Registry
		lf               *lock.File
		in               *install.Installer
		co               *source.Checkout
		versioned, plain skill.Skill
		claude, codex    agent.Agent
	)

	find := func(name string) skill.Skill {
		skills, err := skill.Discover(repoDir)
		Expect(err).NotTo(HaveOccurred())
		for _, s := range skills {
			if s.Name == name {
				return s
			}
		}
		Fail("skill not found: " + name)
		return skill.Skill{}
	}

	BeforeEach(func() {
		repoDir = GinkgoT().TempDir()
		project = GinkgoT().TempDir()
		Expect(testutil.WriteFiles(repoDir, map[string]string{
			"skills/pdf/SKILL.md":       testutil.SkillMD("pdf", "PDF", "1.0.0"),
			"skills/pdf/scripts/run.sh": "echo",
			"skills/web/SKILL.md":       testutil.SkillMD("web", "Web", ""),
			"skills/web/.git/HEAD":      "ignored",
		})).To(Succeed())
		reg = agent.NewRegistry(nil)
		claude, _ = reg.Get("claude-code")
		codex, _ = reg.Get("codex")
		var err error
		lf, err = lock.Load(filepath.Join(project, lock.FileName))
		Expect(err).NotTo(HaveOccurred())
		in = &install.Installer{Target: install.Target{Scope: agent.ScopeProject, ProjectRoot: project}, Lock: lf, Registry: reg}
		co = &source.Checkout{URL: "https://example.com/skills", Ref: "main", Commit: "abc"}
		versioned, plain = find("pdf"), find("web")
	})

	It("copies the skill into every agent directory and records it", func() {
		e, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude, codex}})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(project, ".claude", "skills", "pdf", "scripts", "run.sh")).To(BeAnExistingFile())
		Expect(filepath.Join(project, ".agents", "skills", "pdf", "SKILL.md")).To(BeAnExistingFile())
		Expect(e.Tracking).To(Equal(lock.TrackVersion))
		Expect(e.Version).To(Equal("1.0.0"))
		want, _ := skill.Hash(versioned.Dir)
		Expect(e.Hash).To(Equal(want), "the installed content is recorded to detect local edits")
		Expect(e.Agents).To(Equal([]string{"claude-code", "codex"}))
		Expect(e.Path).To(Equal("skills/pdf"))
		Expect(e.Commit).To(Equal("abc"))
		_, ok := lf.Get("pdf")
		Expect(ok).To(BeTrue())
	})

	It("stores a content hash when hash tracking is chosen", func() {
		e, err := in.Install(install.Request{Skill: plain, Checkout: co, Agents: []agent.Agent{claude}, Tracking: lock.TrackHash})
		Expect(err).NotTo(HaveOccurred())
		want, _ := skill.Hash(plain.Dir)
		Expect(e.Hash).To(Equal(want))
		Expect(e.Version).To(BeEmpty())
		Expect(filepath.Join(project, ".claude", "skills", "web", ".git")).NotTo(BeAnExistingFile())
	})

	It("records untracked skills without version or hash", func() {
		e, err := in.Install(install.Request{Skill: plain, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Tracking).To(Equal(lock.TrackNone))
		Expect(e.Hash).To(BeEmpty())
	})

	It("rejects version tracking for skills without a version", func() {
		_, err := in.Install(install.Request{Skill: plain, Checkout: co, Agents: []agent.Agent{claude}, Tracking: lock.TrackVersion})
		Expect(err).To(MatchError(ContainSubstring("no version")))
	})

	It("refuses to overwrite unmanaged directories unless forced", func() {
		Expect(testutil.WriteFiles(project, map[string]string{".claude/skills/pdf/SKILL.md": "mine"})).To(Succeed())
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).To(MatchError(install.ErrConflict))
		_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}, Force: true})
		Expect(err).NotTo(HaveOccurred())
	})

	It("refuses to replace a skill from a different source", func() {
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		other := &source.Checkout{URL: "https://example.com/other"}
		_, err = in.Install(install.Request{Skill: versioned, Checkout: other, Agents: []agent.Agent{claude}})
		Expect(err).To(MatchError(install.ErrConflict))
	})

	It("reinstalls managed skills and drops deselected agents", func() {
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude, codex}})
		Expect(err).NotTo(HaveOccurred())
		_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(project, ".agents", "skills", "pdf")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(project, ".claude", "skills", "pdf")).To(BeADirectory())
	})

	It("uninstalls from all agents and the lockfile", func() {
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude, codex}})
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Uninstall("pdf")).To(Succeed())
		Expect(filepath.Join(project, ".claude", "skills", "pdf")).NotTo(BeAnExistingFile())
		Expect(filepath.Join(project, ".agents", "skills", "pdf")).NotTo(BeAnExistingFile())
		Expect(lf.Skills).To(BeEmpty())
		Expect(in.Uninstall("pdf")).To(HaveOccurred())
	})

	It("installs globally below the home directory", func() {
		home := GinkgoT().TempDir()
		in.Target = install.Target{Scope: agent.ScopeGlobal, Home: home}
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(filepath.Join(home, ".claude", "skills", "pdf", "SKILL.md")).To(BeAnExistingFile())
	})

	It("deduplicates agents sharing a directory", func() {
		universal, _ := reg.Get("agents")
		dirs, err := in.Target.Dirs([]agent.Agent{codex, universal})
		Expect(err).NotTo(HaveOccurred())
		Expect(dirs).To(HaveLen(1))
	})

	It("requires at least one agent", func() {
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co})
		Expect(err).To(HaveOccurred())
	})

	DescribeTable("DirName",
		func(in, want string) { Expect(install.DirName(in)).To(Equal(want)) },
		Entry("plain", "pdf", "pdf"),
		Entry("spaces and case", "My Cool Skill", "my-cool-skill"),
		Entry("path traversal", "../../etc", "etc"),
		Entry("empty", "  ", "skill"),
	)

	It("offers tracking modes depending on the version", func() {
		Expect(install.AvailableTracking(versioned)).To(ContainElement(lock.TrackVersion))
		Expect(install.AvailableTracking(plain)).NotTo(ContainElement(lock.TrackVersion))
	})

	It("keeps executable permissions", func() {
		Expect(os.Chmod(filepath.Join(versioned.Dir, "scripts", "run.sh"), 0o755)).To(Succeed())
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		fi, err := os.Stat(filepath.Join(project, ".claude", "skills", "pdf", "scripts", "run.sh"))
		Expect(err).NotTo(HaveOccurred())
		if filepath.Separator == '/' {
			Expect(fi.Mode().Perm() & 0o100).NotTo(BeZero())
		}
	})

	Describe("Modified", func() {
		It("detects local edits in any agent copy", func() {
			e, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude, codex}})
			Expect(err).NotTo(HaveOccurred())
			Expect(in.Modified(e)).To(BeFalse())
			Expect(os.WriteFile(filepath.Join(project, ".agents", "skills", "pdf", "notes.md"), []byte("mine"), 0o644)).To(Succeed())
			Expect(in.Modified(e)).To(BeTrue())
		})

		It("ignores untracked skills and missing copies", func() {
			e, err := in.Install(install.Request{Skill: plain, Checkout: co, Agents: []agent.Agent{claude}})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Hash).To(BeEmpty())
			Expect(in.Modified(e)).To(BeFalse())

			e, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
			Expect(err).NotTo(HaveOccurred())
			Expect(os.RemoveAll(filepath.Join(project, ".claude", "skills", "pdf"))).To(Succeed())
			Expect(in.Modified(e)).To(BeFalse())
		})
	})

	It("uses an explicit directory name and rejects unsafe ones", func() {
		e, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}, Name: "My PDF"})
		Expect(err).NotTo(HaveOccurred())
		Expect(e.Name).To(Equal("My PDF"))
		Expect(filepath.Join(project, ".claude", "skills", "My PDF", "SKILL.md")).To(BeAnExistingFile())
		_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}, Name: "../escape"})
		Expect(err).To(MatchError(ContainSubstring("invalid skill name")))
		_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}, Name: ".ssh"})
		Expect(err).To(MatchError(ContainSubstring("invalid skill name")))
	})

	Describe("symlinked skill directories in a project", func() {
		var victim string

		BeforeEach(func() {
			if runtime.GOOS == "windows" {
				Skip("symlinks need privileges on Windows")
			}
			// A cloned repository can commit .claude/skills -> anywhere.
			victim = GinkgoT().TempDir()
			Expect(testutil.WriteFiles(victim, map[string]string{"pdf/data.txt": "keep"})).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(project, ".claude"), 0o755)).To(Succeed())
			Expect(os.Symlink(victim, filepath.Join(project, ".claude", "skills"))).To(Succeed())
		})

		It("refuses to install through them", func() {
			_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}, Force: true})
			Expect(err).To(MatchError(ContainSubstring("symlink")))
			Expect(filepath.Join(victim, "pdf", "data.txt")).To(BeAnExistingFile())
		})

		It("refuses to uninstall through them", func() {
			lf.Upsert(lock.Entry{Name: "pdf", Agents: []string{"claude-code"}, Tracking: lock.TrackNone})
			Expect(in.Uninstall("pdf")).To(MatchError(ContainSubstring("symlink")))
			Expect(filepath.Join(victim, "pdf", "data.txt")).To(BeAnExistingFile())
		})

		It("does not report their content as unmanaged skills", func() {
			Expect(testutil.WriteFiles(victim, map[string]string{"other/SKILL.md": testutil.SkillMD("other", "x", "")})).To(Succeed())
			Expect(in.Unmanaged()).To(BeEmpty())
		})
	})

	It("leaves every copy untouched when one of them cannot be written", func() {
		_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(os.WriteFile(filepath.Join(project, ".claude", "skills", "pdf", "marker"), []byte("old copy"), 0o644)).To(Succeed())
		// A file where the codex skills directory should be makes that copy fail.
		Expect(os.WriteFile(filepath.Join(project, ".agents"), []byte("not a dir"), 0o644)).To(Succeed())

		_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude, codex}, Force: true})
		Expect(err).To(HaveOccurred())
		Expect(filepath.Join(project, ".claude", "skills", "pdf", "marker")).To(BeAnExistingFile(), "first copy was not replaced")
		e, _ := lf.Get("pdf")
		Expect(e.Agents).To(Equal([]string{"claude-code"}), "lockfile unchanged")
		leftovers, _ := filepath.Glob(filepath.Join(project, ".claude", "skills", ".pdf.tmp-*"))
		Expect(leftovers).To(BeEmpty())
	})

	Describe("Unmanaged", func() {
		It("finds skills in agent directories that are not in the lockfile", func() {
			_, err := in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/manual/SKILL.md":  testutil.SkillMD("manual", "Copied by hand", "0.1.0"),
				".agents/skills/manual/SKILL.md":  testutil.SkillMD("manual", "Copied by hand", "0.1.0"),
				".agents/skills/other/SKILL.md":   testutil.SkillMD("other", "", ""),
				".agents/skills/pdf/SKILL.md":     testutil.SkillMD("pdf", "unmanaged copy for another agent", ""),
				".claude/skills/notes.txt":        "not a skill",
				".claude/skills/empty/readme.md":  "no SKILL.md",
				".claude/skills/.hidden/SKILL.md": testutil.SkillMD("hidden", "", ""),
			})).To(Succeed())

			found := in.Unmanaged()
			var names []string
			for _, f := range found {
				names = append(names, f.Name)
			}
			Expect(names).To(Equal([]string{"manual", "other", "pdf"}))
			manual := found[0]
			Expect(manual.Agents).To(ContainElements("claude-code", "codex", "agents"))
			Expect(manual.Dirs).To(ConsistOf(
				filepath.Join(project, ".claude", "skills", "manual"),
				filepath.Join(project, ".agents", "skills", "manual"),
			))
			Expect(manual.Version).To(Equal("0.1.0"))

			Expect(install.RemoveFound(manual)).To(Succeed())
			Expect(filepath.Join(project, ".claude", "skills", "manual")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(project, ".claude", "skills", "pdf")).To(BeADirectory(), "managed skills are untouched")
		})

		It("follows symlinked skill directories", func() {
			if filepath.Separator != '/' {
				Skip("symlinks need privileges on Windows")
			}
			Expect(testutil.WriteFiles(project, map[string]string{".agents/skills/linked/SKILL.md": testutil.SkillMD("linked", "", "")})).To(Succeed())
			Expect(os.MkdirAll(filepath.Join(project, ".claude", "skills"), 0o755)).To(Succeed())
			Expect(os.Symlink(filepath.Join(project, ".agents", "skills", "linked"), filepath.Join(project, ".claude", "skills", "linked"))).To(Succeed())
			found := in.Unmanaged()
			Expect(found).To(HaveLen(1))
			Expect(found[0].Agents).To(ContainElement("claude-code"))
		})

		It("returns nothing when agent directories do not exist", func() {
			Expect(in.Unmanaged()).To(BeEmpty())
		})
	})

	Describe("Adopt", func() {
		var found install.Found

		BeforeEach(func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/pdf/SKILL.md":       testutil.SkillMD("pdf", "PDF", "1.0.0"),
				".claude/skills/pdf/scripts/run.sh": "echo",
			})).To(Succeed())
			fs := in.Unmanaged()
			Expect(fs).To(HaveLen(1))
			found = fs[0]
		})

		It("records an identical copy with its commit and keeps the files", func() {
			e, err := in.Adopt(install.AdoptRequest{Found: found, Checkout: co, Upstream: &versioned})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Source).To(Equal(co.URL))
			Expect(e.Path).To(Equal("skills/pdf"))
			Expect(e.Commit).To(Equal("abc"))
			Expect(e.Tracking).To(Equal(lock.TrackVersion))
			Expect(e.Version).To(Equal("1.0.0"))
			Expect(e.Agents).To(Equal([]string{"claude-code"}))
			Expect(in.Unmanaged()).To(BeEmpty())
			Expect(filepath.Join(project, ".claude", "skills", "pdf", "scripts", "run.sh")).To(BeAnExistingFile())
		})

		It("hashes symlinked copies by their content", func() {
			if filepath.Separator != '/' {
				Skip("symlinks need privileges on Windows")
			}
			real := filepath.Join(project, ".agents", "skills", "pdf")
			Expect(os.MkdirAll(filepath.Dir(real), 0o755)).To(Succeed())
			Expect(os.Rename(found.Dirs[0], real)).To(Succeed())
			Expect(os.Symlink(real, found.Dirs[0])).To(Succeed())
			e, err := in.Adopt(install.AdoptRequest{Found: found, Checkout: co, Upstream: &versioned, Tracking: lock.TrackHash})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Commit).To(Equal("abc"), "the linked copy is identical to upstream")
			want, _ := skill.Hash(versioned.Dir)
			Expect(e.Hash).To(Equal(want))
		})

		It("stores the local hash and no commit when the copy differs", func() {
			Expect(os.WriteFile(filepath.Join(found.Dirs[0], "scripts", "run.sh"), []byte("local change"), 0o644)).To(Succeed())
			e, err := in.Adopt(install.AdoptRequest{Found: found, Checkout: co, Upstream: &versioned, Tracking: lock.TrackHash})
			Expect(err).NotTo(HaveOccurred())
			local, _ := skill.Hash(found.Dirs[0])
			Expect(e.Hash).To(Equal(local))
			Expect(e.Commit).To(BeEmpty())
		})

		It("uses 0.0.0 when the local copy has no version", func() {
			Expect(os.WriteFile(filepath.Join(found.Dirs[0], "SKILL.md"), []byte(testutil.SkillMD("pdf", "", "")), 0o644)).To(Succeed())
			found = in.Unmanaged()[0]
			e, err := in.Adopt(install.AdoptRequest{Found: found, Checkout: co, Upstream: &versioned, Tracking: lock.TrackVersion})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Version).To(Equal("0.0.0"))
		})

		It("rejects version tracking when upstream has no version", func() {
			_, err := in.Adopt(install.AdoptRequest{Found: found, Checkout: co, Upstream: &plain, Tracking: lock.TrackVersion})
			Expect(err).To(MatchError(ContainSubstring("no version")))
		})

		It("adopts local-only skills without tracking", func() {
			e, err := in.Adopt(install.AdoptRequest{Found: found})
			Expect(err).NotTo(HaveOccurred())
			Expect(e.Source).To(BeEmpty())
			Expect(e.Tracking).To(Equal(lock.TrackNone))
			_, err = in.Adopt(install.AdoptRequest{Found: found})
			Expect(err).To(MatchError(install.ErrConflict))

			By("allowing a later install from any source to take over")
			_, err = in.Install(install.Request{Skill: versioned, Checkout: co, Agents: []agent.Agent{claude}})
			Expect(err).NotTo(HaveOccurred())
		})

		It("refuses tracking for local-only skills", func() {
			_, err := in.Adopt(install.AdoptRequest{Found: found, Tracking: lock.TrackHash})
			Expect(err).To(HaveOccurred())
		})
	})
})
