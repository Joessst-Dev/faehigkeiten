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

	It("lists skills that were not installed by faehigkeiten", func() {
		Expect(testutil.WriteFiles(project, map[string]string{
			".claude/skills/handmade/SKILL.md": testutil.SkillMD("handmade", "", ""),
		})).To(Succeed())
		code, out, _ := run("list", "-p", project)
		Expect(code).To(Equal(0))
		Expect(out).To(MatchRegexp(`handmade\s+not managed\s+claude-code`))
	})

	Describe("adopt", func() {
		BeforeEach(func() {
			Expect(testutil.SeedIndex(filepath.Join(filepath.Dir(cfgDir), "cache"),
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
