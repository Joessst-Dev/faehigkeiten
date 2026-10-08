package config_test

import (
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
		Expect(cfg.Repos).To(BeEmpty())
	})

	It("round-trips repos, default agents and agent overrides", func() {
		cfg := &config.Config{
			DefaultAgents: []string{"claude-code"},
			Agents:        map[string]config.AgentOverride{"cursor": {ProjectDir: ".cursor/rules"}},
		}
		Expect(cfg.AddRepo(config.Repo{Name: "mine", URL: "https://example.com/a.git", Ref: "dev"})).To(BeTrue())
		Expect(cfg.Save()).To(Succeed())

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
})
