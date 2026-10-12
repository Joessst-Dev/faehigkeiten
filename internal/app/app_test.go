package app_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
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

var _ = Describe("NoSourceError", func() {
	cand := func(name string) app.Candidate {
		return app.Candidate{Entry: registry.Entry{Name: "pdf", Repo: registry.Repo{Name: name}}}
	}

	It("explains why no source was chosen", func() {
		Expect((&app.NoSourceError{Name: "pdf"}).Error()).To(Equal("no known repository contains a skill named pdf"))
		Expect((&app.NoSourceError{Name: "pdf", Candidates: []app.Candidate{cand("a/b")}}).Error()).
			To(Equal("pdf differs from its only source, a/b"))
		Expect((&app.NoSourceError{Name: "pdf", Candidates: []app.Candidate{cand("a/b"), cand("c/d")}}).Error()).
			To(ContainSubstring("2 repositories contain pdf"))
	})
})

var _ = Describe("Resolver", func() {
	var (
		a       *app.App
		origin  string
		project string
	)

	found := func(name string, dirs ...string) install.Found {
		return install.Found{Name: name, Agents: []string{"claude-code"}, Dirs: dirs}
	}

	BeforeEach(func() {
		base := GinkgoT().TempDir()
		GinkgoT().Setenv(config.EnvConfigDir, filepath.Join(base, "config"))
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(base, "cache"))
		GinkgoT().Setenv("HOME", filepath.Join(base, "home"))
		GinkgoT().Setenv("USERPROFILE", filepath.Join(base, "home"))
		origin = filepath.Join(base, "origin")
		_, err := testutil.InitRepo(origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			"skills/beta/SKILL.md":  testutil.SkillMD("beta", "Unversioned", ""),
			"skills/gamma/SKILL.md": testutil.SkillMD("gamma", "Third", ""),
		})
		Expect(err).NotTo(HaveOccurred())
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.SeedIndex(cfg, filepath.Join(base, "cache"), registry.Repo{Name: "fixture/skills", URL: origin})).To(Succeed())
		a, err = app.New()
		Expect(err).NotTo(HaveOccurred())

		project = filepath.Join(base, "project")
		Expect(testutil.WriteFiles(project, map[string]string{
			"claude/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			"agents/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned", "1.0.0"),
			"claude/beta/SKILL.md":  testutil.SkillMD("beta", "Unversioned", ""),
			"agents/beta/SKILL.md":  testutil.SkillMD("beta", "edited", ""),
			"claude/gamma/SKILL.md": testutil.SkillMD("gamma", "Third", ""),
			"claude/mine/SKILL.md":  testutil.SkillMD("mine", "", ""),
		})).To(Succeed())
	})

	dir := func(agent, name string) string { return filepath.Join(project, agent, name) }

	It("loads each repository and hashes each directory once per run", func() {
		r := a.NewResolver()
		opts := app.AdoptOptions{IdenticalOnly: true}
		for _, f := range []install.Found{
			found("alpha", dir("claude", "alpha"), dir("agents", "alpha")),
			found("gamma", dir("claude", "gamma")),
		} {
			req, err := r.ResolveAdopt(context.Background(), f, opts)
			Expect(err).NotTo(HaveOccurred())
			Expect(req.Checkout.URL).To(Equal(origin))
			Expect(req.Upstream.Path).To(Equal("skills/" + f.Name))
			Expect(req.LocalHash).NotTo(BeEmpty())
			Expect(req.LocalHash).To(Equal(req.UpstreamHash))
		}
		loads, hashes := r.Counts()
		Expect(loads).To(Equal(1))
		Expect(hashes).To(Equal(5), "three local copies and two upstream skills")
	})

	It("still checks every local copy", func() {
		_, err := a.NewResolver().ResolveAdopt(context.Background(),
			found("beta", dir("claude", "beta"), dir("agents", "beta")), app.AdoptOptions{IdenticalOnly: true})
		var nse *app.NoSourceError
		Expect(errors.As(err, &nse)).To(BeTrue())
		Expect(nse.Candidates).To(HaveLen(1))
	})
})

var _ = Describe("Resolver.ResolveAll", func() {
	var (
		a       *app.App
		project string
	)

	BeforeEach(func() {
		base := GinkgoT().TempDir()
		GinkgoT().Setenv(config.EnvConfigDir, filepath.Join(base, "config"))
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(base, "cache"))
		GinkgoT().Setenv("HOME", filepath.Join(base, "home"))
		GinkgoT().Setenv("USERPROFILE", filepath.Join(base, "home"))
		origin := filepath.Join(base, "origin")
		files := map[string]string{}
		local := map[string]string{}
		for i := range 20 {
			name := fmt.Sprintf("s%02d", i)
			files["skills/"+name+"/SKILL.md"] = testutil.SkillMD(name, "upstream", "")
			desc := "upstream"
			if i%3 == 1 {
				desc = "edited"
			}
			local[name+"/SKILL.md"] = testutil.SkillMD(name, desc, "")
		}
		local["mine/SKILL.md"] = testutil.SkillMD("mine", "", "")
		_, err := testutil.InitRepo(origin, files)
		Expect(err).NotTo(HaveOccurred())
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.SeedIndex(cfg, filepath.Join(base, "cache"), registry.Repo{Name: "fixture/skills", URL: origin})).To(Succeed())
		a, err = app.New()
		Expect(err).NotTo(HaveOccurred())
		project = filepath.Join(base, "project")
		Expect(testutil.WriteFiles(project, local)).To(Succeed())
	})

	all := func() []install.Found {
		var out []install.Found
		for i := range 20 {
			name := fmt.Sprintf("s%02d", i)
			out = append(out, install.Found{Name: name, Dirs: []string{filepath.Join(project, name)}})
		}
		return append(out, install.Found{Name: "mine", Dirs: []string{filepath.Join(project, "mine")}})
	}

	It("returns results in input order", func() {
		r := a.NewResolver()
		found := all()
		res := r.ResolveAll(context.Background(), found, app.AdoptOptions{IdenticalOnly: true})
		Expect(res).To(HaveLen(len(found)))
		for i, got := range res {
			switch {
			case found[i].Name == "mine":
				Expect(got.Err).To(MatchError(ContainSubstring("no known repository")))
			case i%3 == 1:
				Expect(got.Err).To(MatchError(ContainSubstring("differs from its only source")), found[i].Name)
			default:
				Expect(got.Err).NotTo(HaveOccurred(), found[i].Name)
				Expect(got.Req.Found.Name).To(Equal(found[i].Name))
				Expect(got.Req.Upstream.Path).To(Equal("skills/" + found[i].Name))
			}
		}
		loads, _ := r.Counts()
		Expect(loads).To(Equal(1))
	})

	It("stops when the context is cancelled", func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		for _, got := range a.NewResolver().ResolveAll(ctx, all(), app.AdoptOptions{IdenticalOnly: true}) {
			Expect(got.Err).To(MatchError(context.Canceled))
		}
	})
})
