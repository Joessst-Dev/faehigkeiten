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

	It("round-trips repos, default agents, agent overrides and favourites", func() {
		cfg := &config.Config{}
		cfg.SetDefaultAgents([]string{"claude-code"})
		cfg.SetAgent("cursor", config.AgentOverride{ProjectDir: ".cursor/rules"})
		Expect(cfg.AddRepo(config.Repo{Name: "mine", URL: "https://example.com/a.git", Ref: "dev"})).To(BeTrue())
		Expect(cfg.AddFavourite(config.Favourite{Name: "pdf", URL: "https://example.com/a.git", Path: "skills/pdf"})).To(BeTrue())
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
favourites:
  - name: pdf
    url: https://example.com/a.git
    path: skills/pdf
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

	It("deduplicates and removes favourites by URL and path", func() {
		cfg := &config.Config{}
		Expect(cfg.AddFavourite(config.Favourite{Name: "a", URL: "u", Path: "a"})).To(BeTrue())
		Expect(cfg.AddFavourite(config.Favourite{Name: "again", URL: "u", Path: "a", Ref: "dev"})).To(BeFalse())
		Expect(cfg.AddFavourite(config.Favourite{Name: "b", URL: "u", Path: "b"})).To(BeTrue())
		Expect(cfg.IsFavourite("u", "a")).To(BeTrue())
		Expect(cfg.IsFavourite("v", "a")).To(BeFalse())
		Expect(cfg.RemoveFavourite("u", "a")).To(BeTrue())
		Expect(cfg.RemoveFavourite("u", "a")).To(BeFalse())
		Expect(cfg.FavouriteList()).To(Equal([]config.Favourite{{Name: "b", URL: "u", Path: "b"}}))
	})

	It("sorts favourites by name, then by source and path", func() {
		favs := []config.Favourite{
			{Name: "pdf", URL: "b", Path: "p"},
			{Name: "Docx", URL: "a", Path: "d"},
			{Name: "pdf", URL: "A", Path: "q"},
			{Name: "pdf", URL: "a", Path: "p"},
		}
		config.SortFavourites(favs)
		Expect(favs).To(Equal([]config.Favourite{
			{Name: "Docx", URL: "a", Path: "d"},
			{Name: "pdf", URL: "a", Path: "p"},
			{Name: "pdf", URL: "A", Path: "q"},
			{Name: "pdf", URL: "b", Path: "p"},
		}))
	})

	It("compares favourite URLs ignoring case", func() {
		cfg := &config.Config{}
		Expect(cfg.AddFavourite(config.Favourite{URL: "https://github.com/Acme/Skills", Path: "a"})).To(BeTrue())
		Expect(cfg.AddFavourite(config.Favourite{URL: "https://github.com/acme/skills", Path: "a"})).To(BeFalse())
		Expect(cfg.IsFavourite("https://github.com/ACME/skills", "a")).To(BeTrue())
		Expect(cfg.IsFavourite("https://github.com/acme/skills", "A")).To(BeFalse(), "paths are case-sensitive")
		Expect(cfg.RemoveFavourite("https://github.com/acme/skills", "a")).To(BeTrue())
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
		o.AddFavourite(config.Favourite{URL: "b", Path: "p"})
		cfg.Replace(o)
		Expect(cfg).To(Equal(o))
		o.AddRepo(config.Repo{URL: "c"})
		Expect(cfg.RepoList()).To(HaveLen(1), "nothing is shared")
	})
})
