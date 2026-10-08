package agent_test

import (
	"os"
	"path/filepath"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
)

var _ = Describe("Agent", func() {
	claude := agent.Agent{ID: "claude-code", ProjectDir: ".claude/skills", GlobalDir: ".claude/skills"}

	It("resolves project and global directories", func() {
		Expect(claude.Dir(agent.ScopeProject, "/repo", "/home/u")).To(Equal(filepath.Join("/repo", ".claude", "skills")))
		Expect(claude.Dir(agent.ScopeGlobal, "", "/home/u")).To(Equal(filepath.Join("/home/u", ".claude", "skills")))
	})

	It("requires a project root for project scope", func() {
		_, err := claude.Dir(agent.ScopeProject, "", "/home/u")
		Expect(err).To(HaveOccurred())
	})

	It("rejects unsupported scopes", func() {
		_, err := agent.Agent{ID: "x", ProjectDir: "a"}.Dir(agent.ScopeGlobal, "", "/h")
		Expect(err).To(HaveOccurred())
		_, err = claude.Dir("bogus", "/r", "/h")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("Registry", func() {
	It("contains the builtin agents with unique IDs", func() {
		r := agent.NewRegistry(nil)
		seen := map[string]bool{}
		for _, a := range r.All() {
			Expect(seen).NotTo(HaveKey(a.ID))
			seen[a.ID] = true
		}
		Expect(seen).To(HaveKey("claude-code"))
	})

	It("applies overrides and adds custom agents", func() {
		abs := filepath.Join(GinkgoT().TempDir(), "skills")
		r := agent.NewRegistry(&config.Config{Agents: map[string]config.AgentOverride{
			"claude-code": {GlobalDir: abs},
			"my-agent":    {Name: "Mine", ProjectDir: ".mine/skills"},
		}})
		c, ok := r.Get("claude-code")
		Expect(ok).To(BeTrue())
		Expect(c.GlobalDir).To(Equal(abs))
		Expect(c.Dir(agent.ScopeGlobal, "", "/home/u")).To(Equal(abs))
		m, ok := r.Get("my-agent")
		Expect(ok).To(BeTrue())
		Expect(m.Name).To(Equal("Mine"))
	})

	It("looks up agents by ID", func() {
		r := agent.NewRegistry(nil)
		as, err := r.Lookup([]string{"claude-code", "cursor"})
		Expect(err).NotTo(HaveOccurred())
		Expect(as).To(HaveLen(2))
		_, err = r.Lookup([]string{"nope"})
		Expect(err).To(MatchError(ContainSubstring("nope")))
	})

	It("detects agents already configured in a project", func() {
		root := GinkgoT().TempDir()
		Expect(os.MkdirAll(filepath.Join(root, ".claude"), 0o755)).To(Succeed())
		Expect(agent.NewRegistry(nil).Detect(agent.ScopeProject, root, "")).To(Equal([]string{"claude-code"}))
	})
})
