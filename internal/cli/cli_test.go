package cli_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/cli"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

var _ = Describe("CLI", func() {
	var (
		project, origin, home, cfgDir string
		repo                          *git.Repository
	)

	run := func(args ...string) (int, string, string) {
		var out, errOut bytes.Buffer
		code := cli.Execute(context.Background(), cli.BuildInfo{Version: "test"}, args, &out, &errOut)
		return code, out.String(), errOut.String()
	}

	BeforeEach(func() {
		base := GinkgoT().TempDir()
		cfgDir = filepath.Join(base, "config")
		home = filepath.Join(base, "home")
		GinkgoT().Setenv(config.EnvConfigDir, cfgDir)
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(base, "cache"))
		GinkgoT().Setenv("HOME", home)
		GinkgoT().Setenv("USERPROFILE", home)
		project = filepath.Join(base, "project")
		Expect(os.MkdirAll(project, 0o755)).To(Succeed())
		origin = filepath.Join(base, "origin")
		var err error
		repo, err = testutil.InitRepo(origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			"skills/beta/SKILL.md":  testutil.SkillMD("beta", "Unversioned", ""),
		})
		Expect(err).NotTo(HaveOccurred())
	})

	It("prints the version", func() {
		code, out, _ := run("--version")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("test"))
	})

	It("lists skills of a source", func() {
		code, out, _ := run("install", origin, "--list")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("alpha"))
		Expect(out).To(ContainSubstring("1.0.0"))
		Expect(out).To(ContainSubstring("skills/beta"))
	})

	Describe("licenses", func() {
		const mit = "MIT License\n\nPermission is hereby granted, free of charge, to any person\nThe above copyright notice and this permission notice shall be included\n"

		BeforeEach(func() {
			Expect(testutil.Commit(repo, origin, map[string]string{
				"LICENSE":                 "\x1b[31m" + mit,
				"skills/beta/LICENSE.txt": "Beta terms\n",
			}, "licenses")).To(Succeed())
		})

		It("shows them in lists", func() {
			code, out, _ := run("install", origin, "--list")
			Expect(code).To(Equal(0))
			Expect(out).To(MatchRegexp(`alpha\s+1.0.0\s+MIT\s+skills/alpha`))
			Expect(out).To(MatchRegexp(`beta\s+-\s+custom\s+skills/beta`))

			code, _, errOut := run("install", origin, "--all", "-p", project, "-a", "claude-code")
			Expect(code).To(Equal(0), errOut)
			code, out, _ = run("list", "-p", project)
			Expect(code).To(Equal(0))
			Expect(out).To(MatchRegexp(`alpha\s+version 1.0.0\s+MIT\s+claude-code`))
		})

		It("prints the license of installed skills and of skills in a source", func() {
			code, _, errOut := run("install", origin, "--all", "-p", project, "-a", "claude-code")
			Expect(code).To(Equal(0), errOut)

			code, out, errOut := run("license", "alpha", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(Equal(" [31m"+mit), "the repository license, without escape sequences")

			code, out, errOut = run("license", "beta", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(Equal("Beta terms\n"), "read from the installed copy")

			code, out, errOut = run("license", origin, "alpha")
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("Permission is hereby granted"))

			code, _, errOut = run("license", "missing", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("not installed"))
		})
	})

	It("requires skill names or --all", func() {
		code, _, errOut := run("install", origin, "-p", project)
		Expect(code).To(Equal(1))
		Expect(errOut).To(ContainSubstring("--all"))
	})

	It("installs, lists, checks, updates and removes skills", func() {
		code, out, errOut := run("install", origin, "--all", "-p", project, "-a", "claude-code,codex", "-t", "version")
		Expect(code).To(Equal(0), errOut)
		Expect(errOut).To(ContainSubstring("beta has no version, using hash tracking"))
		Expect(out).To(ContainSubstring("installed alpha (version 1.0.0)"))
		Expect(filepath.Join(project, ".agents", "skills", "beta", "SKILL.md")).To(BeAnExistingFile())

		lf, err := lock.Load(filepath.Join(project, lock.FileName))
		Expect(err).NotTo(HaveOccurred())
		beta, _ := lf.Get("beta")
		Expect(beta.Tracking).To(Equal(lock.TrackHash))

		code, out, _ = run("list", "-p", project)
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("alpha"))
		Expect(out).To(ContainSubstring("claude-code,codex"))

		code, out, _ = run("check", "-p", project, "--exit-code")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("up to date"))

		Expect(testutil.Commit(repo, origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "2.0.0"),
			"skills/beta/new.md":    "content",
		}, "change")).To(Succeed())

		code, out, _ = run("check", "-p", project, "--exit-code")
		Expect(code).To(Equal(10))
		Expect(out).To(ContainSubstring("update available"))

		code, out, _ = run("update", "-p", project)
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("updated alpha (version 2.0.0)"))
		Expect(out).To(ContainSubstring("updated beta"))
		Expect(filepath.Join(project, ".claude", "skills", "beta", "new.md")).To(BeAnExistingFile())

		code, out, _ = run("remove", "alpha", "beta", "-p", project)
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("removed beta"))
		Expect(filepath.Join(project, lock.FileName)).NotTo(BeAnExistingFile())
	})

	Describe("versions", func() {
		var src string
		var commits map[string]string

		BeforeEach(func() {
			src = testutil.FileURL(origin)
			h, err := repo.Head()
			Expect(err).NotTo(HaveOccurred())
			commits = map[string]string{"1.0.0": h.Hash().String()}
			Expect(testutil.Commit(repo, origin, map[string]string{
				"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "2.0.0"),
			}, "bump")).To(Succeed())
			h, err = repo.Head()
			Expect(err).NotTo(HaveOccurred())
			commits["2.0.0"] = h.Hash().String()
		})

		It("lists the versions of a skill", func() {
			code, out, errOut := run("install", src, "alpha", "--versions")
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(MatchRegexp(`2\.0\.0\s+` + commits["2.0.0"][:7] + `\s+\S+\s+\(latest\)`))
			Expect(out).To(MatchRegexp(`1\.0\.0\s+` + commits["1.0.0"][:7]))
		})

		It("explains unknown versions", func() {
			code, _, errOut := run("install", src, "alpha@9.9.9", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("available: 2.0.0, 1.0.0"))
		})

		It("pins a skill at an older version until it is unpinned", func() {
			code, out, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("installed alpha (pinned at 1.0.0)"))
			Expect(os.ReadFile(filepath.Join(project, ".claude", "skills", "alpha", "SKILL.md"))).To(ContainSubstring("version: 1.0.0"))
			lf, err := lock.Load(filepath.Join(project, lock.FileName))
			Expect(err).NotTo(HaveOccurred())
			e, _ := lf.Get("alpha")
			Expect(e.Pinned).To(BeTrue())
			Expect(e.Version).To(Equal("1.0.0"))
			Expect(e.Commit).To(Equal(commits["1.0.0"]))
			Expect(e.Ref).To(Equal("master"))

			code, out, _ = run("check", "-p", project, "--exit-code")
			Expect(code).To(Equal(0), "pinned skills do not count as updates")
			Expect(out).To(ContainSubstring("pinned (newer version available)"))

			code, out, errOut = run("update", "-p", project)
			Expect(code).To(Equal(0))
			Expect(out).To(ContainSubstring("nothing to update"))
			Expect(errOut).To(ContainSubstring("skipping alpha: pinned at 1.0.0"))

			code, out, errOut = run("update", "alpha", "--unpin", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("updated alpha (version 2.0.0)"))
			lf, err = lock.Load(filepath.Join(project, lock.FileName))
			Expect(err).NotTo(HaveOccurred())
			e, _ = lf.Get("alpha")
			Expect(e.Pinned).To(BeFalse())
			Expect(e.Commit).To(Equal(commits["2.0.0"]))
		})

		It("keeps the pin when a pinned skill is installed again without a version", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			code, out, errOut := run("install", src, "alpha", "-p", project, "--force")
			Expect(code).To(Equal(0), errOut)
			Expect(errOut).To(ContainSubstring("alpha is pinned at 1.0.0 and stays there"))
			Expect(out).To(ContainSubstring("installed alpha (pinned at 1.0.0)"))
			Expect(os.ReadFile(filepath.Join(project, ".claude", "skills", "alpha", "SKILL.md"))).To(ContainSubstring("version: 1.0.0"))
		})

		It("replaces the pin when installing from another ref", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			h, err := repo.Head()
			Expect(err).NotTo(HaveOccurred())
			Expect(os.WriteFile(filepath.Join(origin, ".git", "refs", "heads", "dev"), []byte(h.Hash().String()+"\n"), 0o644)).To(Succeed())
			code, out, errOut := run("install", src, "alpha", "--ref", "dev", "-p", project, "--force")
			Expect(code).To(Equal(0), errOut)
			Expect(errOut).NotTo(ContainSubstring("stays there"))
			Expect(out).To(ContainSubstring("installed alpha (version 2.0.0)"))
		})

		It("updates the history for versions after keeping a pin", func() {
			Expect(testutil.Commit(repo, origin, map[string]string{
				"skills/beta/SKILL.md": testutil.SkillMD("beta", "Unversioned", "1.0.0"),
			}, "version beta")).To(Succeed())
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(testutil.Commit(repo, origin, map[string]string{
				"skills/beta/SKILL.md": testutil.SkillMD("beta", "Unversioned", "2.0.0"),
			}, "bump beta")).To(Succeed())
			code, out, errOut := run("install", src, "alpha", "beta@2.0.0", "-p", project, "--force")
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("installed beta (pinned at 2.0.0)"))
		})

		It("unpins skills named @latest with --all", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			code, out, errOut := run("install", src, "--all", "alpha@latest", "-p", project, "--force")
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("installed alpha (version 2.0.0)"))
		})

		It("shares one history between --versions and installs", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			code, _, errOut = run("install", src, "alpha", "--versions")
			Expect(code).To(Equal(0), errOut)
			histories, _ := filepath.Glob(filepath.Join(filepath.Dir(cfgDir), "cache", "repos", "*.history"))
			Expect(histories).To(HaveLen(1))
		})

		It("decides tracking per skill", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "beta", "-p", project, "--track", "hash")
			Expect(code).To(Equal(0), errOut)
			Expect(errOut).To(ContainSubstring("alpha is pinned, using version tracking"))
			lf, err := lock.Load(filepath.Join(project, lock.FileName))
			Expect(err).NotTo(HaveOccurred())
			beta, _ := lf.Get("beta")
			Expect(beta.Tracking).To(Equal(lock.TrackHash))
		})

		It("does not call an older version the latest when the skill lost its version", func() {
			Expect(testutil.Commit(repo, origin, map[string]string{
				"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", ""),
			}, "drop version")).To(Succeed())
			code, out, errOut := run("install", src, "alpha", "--versions")
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("2.0.0"))
			Expect(out).NotTo(ContainSubstring("(latest)"))
		})

		It("reports and protects local changes to pinned skills", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(testutil.WriteFiles(project, map[string]string{".claude/skills/alpha/notes.md": "mine"})).To(Succeed())

			_, out, _ := run("check", "-p", project)
			Expect(out).To(ContainSubstring("pinned (newer version available, locally modified)"))
			Expect(out).NotTo(ContainSubstring("updating overwrites"), "update does not touch pinned skills")

			code, _, errOut = run("update", "alpha", "--unpin", "-p", project)
			Expect(code).To(Equal(0))
			Expect(errOut).To(ContainSubstring("skipping alpha: it was modified locally"))
			Expect(filepath.Join(project, ".claude", "skills", "alpha", "notes.md")).To(BeAnExistingFile())
		})

		It("unpins a skill pinned at the latest version", func() {
			code, _, errOut := run("install", src, "alpha@2.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			code, out, _ := run("update", "alpha", "--unpin", "-p", project)
			Expect(code).To(Equal(0))
			Expect(out).To(ContainSubstring("unpinned alpha"))
		})

		It("removes the pin when installing @latest", func() {
			code, _, errOut := run("install", src, "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			code, out, errOut := run("install", src, "alpha@latest", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("installed alpha (version 2.0.0)"))
		})

		It("installs older versions from a local repository", func() {
			code, out, errOut := run("install", origin, "skills/alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("pinned at 1.0.0"))
		})

		It("rejects --all with a version", func() {
			code, _, errOut := run("install", src, "--all", "alpha@1.0.0", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("--all"))
		})
	})

	It("lists skills that were not installed by faehigkeiten", func() {
		Expect(testutil.WriteFiles(project, map[string]string{
			".claude/skills/handmade/SKILL.md": testutil.SkillMD("handmade", "", ""),
		})).To(Succeed())
		code, out, _ := run("list", "-p", project)
		Expect(code).To(Equal(0))
		Expect(out).To(MatchRegexp(`handmade\s+not managed\s+-\s+claude-code`))
	})

	Describe("adopt", func() {
		BeforeEach(func() {
			cfg, err := config.Load()
			Expect(err).NotTo(HaveOccurred())
			Expect(testutil.SeedIndex(cfg, filepath.Join(filepath.Dir(cfgDir), "cache"),
				registry.Repo{Name: "fixture/skills", URL: origin})).To(Succeed())
		})

		It("finds an identical source automatically and stops listing it as unmanaged", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			})).To(Succeed())
			code, out, errOut := run("adopt", "alpha", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("adopted alpha from origin (version 1.0.0)"))
			_, out, _ = run("list", "-p", project)
			Expect(out).To(MatchRegexp(`alpha\s+version 1.0.0`))
			Expect(out).NotTo(ContainSubstring("not managed"))
			code, _, _ = run("check", "-p", project, "--exit-code")
			Expect(code).To(Equal(0))
		})

		It("reports an update when the adopted copy differs from upstream", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/beta/SKILL.md": testutil.SkillMD("beta", "edited locally", ""),
			})).To(Succeed())
			code, out, errOut := run("adopt", "beta", "-p", project, "--from", origin, "-t", "version")
			Expect(code).To(Equal(0), errOut)
			Expect(errOut).To(ContainSubstring("using hash tracking"))
			Expect(out).To(ContainSubstring("(hash "))
			code, _, _ = run("check", "-p", project, "--exit-code")
			Expect(code).To(Equal(10))
			code, _, _ = run("update", "-p", project)
			Expect(code).To(Equal(0))
			data, err := os.ReadFile(filepath.Join(project, ".claude", "skills", "beta", "SKILL.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring("Unversioned"))
		})

		It("checks every copy and skips copies of managed skills with --all", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
				".agents/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "edited for codex", "1.0.0"),
			})).To(Succeed())
			code, _, errOut := run("adopt", "--all", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("alpha differs from its only source"))

			code, _, errOut = run("adopt", "alpha", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(testutil.WriteFiles(project, map[string]string{
				".gemini/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "stray copy", ""),
			})).To(Succeed())
			code, _, errOut = run("adopt", "--all", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("no unmanaged skills"))
			Expect(errOut).NotTo(ContainSubstring("skipping"))
		})

		It("returns the real error for a single named skill", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			})).To(Succeed())
			code, _, errOut := run("adopt", "alpha", "--from", origin, "--path", "skills/nope", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring(`skill "skills/nope" not found`))
			Expect(errOut).NotTo(ContainSubstring("could not be adopted"))
		})

		It("adopts local-only skills and explains unknown ones", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/mine/SKILL.md": testutil.SkillMD("mine", "", ""),
			})).To(Succeed())
			code, _, errOut := run("adopt", "mine", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("--local"))
			code, out, _ := run("adopt", "mine", "-p", project, "--local")
			Expect(code).To(Equal(0))
			Expect(out).To(ContainSubstring("local only"))
			code, _, errOut = run("adopt", "nope", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring(`no unmanaged skill named "nope"`))
		})

		It("adopts all unmanaged skills, skipping the ones it cannot resolve", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
				".claude/skills/beta/SKILL.md":  testutil.SkillMD("beta", "edited locally", ""),
				".claude/skills/mine/SKILL.md":  testutil.SkillMD("mine", "", ""),
			})).To(Succeed())
			code, _, errOut := run("adopt", "alpha", "--all", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("--all"))

			code, out, errOut := run("adopt", "--all", "-p", project)
			Expect(code).To(Equal(1))
			Expect(out).To(ContainSubstring("adopted alpha from origin"))
			// beta differs from upstream: not adopted from it without being named
			Expect(errOut).To(ContainSubstring("skipping beta: beta differs from its only source"))
			Expect(errOut).To(ContainSubstring("faehigkeiten adopt beta --project " + project + " --from " + origin + " --path skills/beta"))
			Expect(errOut).To(ContainSubstring("skipping mine"))
			Expect(errOut).To(ContainSubstring("2 of 3 skill(s) could not be adopted"))

			lf, err := lock.Load(filepath.Join(project, lock.FileName))
			Expect(err).NotTo(HaveOccurred())
			_, ok := lf.Get("alpha")
			Expect(ok).To(BeTrue())
			_, ok = lf.Get("beta")
			Expect(ok).To(BeFalse())

			Expect(errOut).To(ContainSubstring("faehigkeiten adopt beta --project " + project + " --local"))

			code, _, errOut = run("adopt", "--all", "--ref", "main", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("--ref can only be used with --all together with --from"))

			By("checking explicit sources too")
			code, _, errOut = run("adopt", "--all", "--from", origin, "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("skipping beta: beta differs from its only source"))
			_, ok = lf.Get("beta")
			Expect(ok).To(BeFalse())

			code, out, errOut = run("adopt", "beta", "beta", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("adopted beta from origin (hash "))

			code, out, errOut = run("adopt", "--all", "--local", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(out).To(ContainSubstring("adopted mine (local only"))
		})
	})

	Describe("updating", func() {
		BeforeEach(func() {
			code, _, errOut := run("install", origin, "alpha", "-p", project)
			Expect(code).To(Equal(0), errOut)
			Expect(testutil.Commit(repo, origin, map[string]string{
				"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.1.0"),
			}, "bump")).To(Succeed())
		})

		It("warns about and skips locally modified skills unless forced", func() {
			local := filepath.Join(project, ".claude", "skills", "alpha", "NOTES.md")
			Expect(os.WriteFile(local, []byte("my notes"), 0o644)).To(Succeed())

			_, out, _ := run("check", "-p", project)
			Expect(out).To(ContainSubstring("locally modified, updating overwrites your changes"))

			code, _, errOut := run("update", "-p", project)
			Expect(code).To(Equal(0))
			Expect(errOut).To(ContainSubstring("warning: skipping alpha: it was modified locally"))
			Expect(local).To(BeAnExistingFile())

			code, out, errOut = run("update", "-p", project, "--force")
			Expect(code).To(Equal(0), errOut)
			Expect(errOut).To(ContainSubstring("warning: overwrote local changes to alpha"))
			Expect(out).To(ContainSubstring("updated alpha (version 1.1.0)"))
			Expect(local).NotTo(BeAnExistingFile())
		})

		It("exits non-zero when a skill cannot be checked", func() {
			Expect(os.RemoveAll(origin)).To(Succeed())
			code, out, _ := run("check", "-p", project, "--exit-code")
			Expect(code).To(Equal(1))
			Expect(out).To(ContainSubstring("error:"))
			code, _, errOut := run("update", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("alpha:"))
		})

		It("rejects skills that are not installed", func() {
			code, _, errOut := run("update", "nope", "-p", project)
			Expect(code).To(Equal(1))
			Expect(errOut).To(ContainSubstring("skill nope is not installed"))
		})
	})

	It("installs globally", func() {
		code, _, errOut := run("install", origin, "alpha", "-g")
		Expect(code).To(Equal(0), errOut)
		Expect(filepath.Join(home, ".claude", "skills", "alpha", "SKILL.md")).To(BeAnExistingFile())
		Expect(filepath.Join(cfgDir, lock.GlobalFileName)).To(BeAnExistingFile())
	})

	It("manages repositories", func() {
		code, out, _ := run("repo", "add", "me/skills", "--ref", "dev")
		Expect(code).To(Equal(0))
		Expect(out).To(ContainSubstring("https://github.com/me/skills"))
		code, _, errOut := run("repo", "add", "me/skills")
		Expect(code).To(Equal(1))
		Expect(errOut).To(ContainSubstring("already"))
		_, out, _ = run("repo", "list")
		Expect(out).To(ContainSubstring("custom"))
		Expect(out).To(ContainSubstring("anthropics/skills"))
		code, _, _ = run("repo", "remove", "me/skills")
		Expect(code).To(Equal(0))
	})

	It("lists agents", func() {
		_, out, _ := run("agents")
		Expect(out).To(ContainSubstring("claude-code"))
		Expect(out).To(ContainSubstring(".github/skills"))
	})

	It("rejects unknown agents", func() {
		code, _, errOut := run("install", origin, "alpha", "-p", project, "-a", "vim")
		Expect(code).To(Equal(1))
		Expect(errOut).To(ContainSubstring(`unknown agent "vim"`))
	})
})
