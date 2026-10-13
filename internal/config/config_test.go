package config_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
)

var _ = Describe("Config", func() {
	var dir string

	BeforeEach(func() {
		dir = GinkgoT().TempDir()
		GinkgoT().Setenv(config.EnvConfigDir, dir)
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(dir, "cache"))
	})

	It("honours directory overrides from the environment", func() {
		Expect(config.Dir()).To(Equal(dir))
		Expect(config.CacheDir()).To(Equal(filepath.Join(dir, "cache")))
		Expect(config.Path()).To(Equal(filepath.Join(dir, "config.yaml")))
	})

	It("returns an empty config when no file exists", func() {
		cfg, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(cfg.RepoList()).To(BeEmpty())
	})

	It("round-trips repos, default agents and agent overrides", func() {
		cfg := &config.Config{}
		cfg.SetDefaultAgents([]string{"claude-code"})
		cfg.SetAgent("cursor", config.AgentOverride{ProjectDir: ".cursor/rules"})
		Expect(cfg.AddRepo(config.Repo{Name: "mine", URL: "https://example.com/a.git", Ref: "dev"})).To(BeTrue())
		Expect(cfg.Save()).To(Succeed())
		p, err := config.Path()
		Expect(err).NotTo(HaveOccurred())
		data, err := os.ReadFile(p)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(data)).To(Equal(`repos:
  - name: mine
    url: https://example.com/a.git
    ref: dev
default_agents:
  - claude-code
agents:
  cursor:
    project_dir: .cursor/rules
`))

		loaded, err := config.Load()
		Expect(err).NotTo(HaveOccurred())
		Expect(loaded).To(Equal(cfg))
	})

	It("deduplicates and removes repos by URL", func() {
		cfg := &config.Config{}
		Expect(cfg.AddRepo(config.Repo{URL: "u"})).To(BeTrue())
		Expect(cfg.AddRepo(config.Repo{URL: "u", Name: "again"})).To(BeFalse())
		Expect(cfg.RemoveRepo("u")).To(BeTrue())
		Expect(cfg.RemoveRepo("u")).To(BeFalse())
	})

	It("updates repos unless the new URL is taken", func() {
		cfg := &config.Config{}
		cfg.AddRepo(config.Repo{URL: "a"})
		cfg.AddRepo(config.Repo{URL: "b"})
		Expect(cfg.UpdateRepo("a", config.Repo{URL: "b"})).To(MatchError(config.ErrRepoExists))
		Expect(cfg.UpdateRepo("missing", config.Repo{URL: "c"})).To(MatchError(config.ErrRepoNotFound))
		Expect(cfg.UpdateRepo("a", config.Repo{Name: "x", URL: "a", Ref: "dev"})).To(Succeed())
		Expect(cfg.UpdateRepo("b", config.Repo{URL: "c"})).To(Succeed())
		Expect(cfg.RepoList()).To(Equal([]config.Repo{{Name: "x", URL: "a", Ref: "dev"}, {URL: "c"}}))
	})

	It("sets and removes agent overrides and default agents", func() {
		cfg := &config.Config{}
		cfg.SetAgent("x", config.AgentOverride{Name: "X"})
		Expect(cfg.AgentOverrides()).To(HaveKeyWithValue("x", config.AgentOverride{Name: "X"}))
		Expect(cfg.RemoveAgent("x")).To(BeTrue())
		Expect(cfg.RemoveAgent("x")).To(BeFalse())
		Expect(cfg.AgentOverrides()).To(BeEmpty())

		cfg.SetDefaultAgents([]string{"a", "b"})
		Expect(cfg.DefaultAgentList()).To(Equal([]string{"a", "b"}))
		cfg.SetDefaultAgents(nil)
		Expect(cfg.DefaultAgentList()).To(BeEmpty())
	})

	It("replaces all settings", func() {
		cfg := &config.Config{}
		cfg.AddRepo(config.Repo{URL: "a"})
		cfg.SetDefaultAgents([]string{"x"})
		o := &config.Config{}
		o.AddRepo(config.Repo{URL: "b"})
		o.SetAgent("y", config.AgentOverride{Name: "Y"})
		cfg.Replace(o)
		Expect(cfg).To(Equal(o))
		o.AddRepo(config.Repo{URL: "c"})
		Expect(cfg.RepoList()).To(HaveLen(1), "nothing is shared")
	})
})
