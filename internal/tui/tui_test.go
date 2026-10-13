package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/go-git/go-git/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/testutil"
)

// driver feeds messages into the model and runs the resulting commands
// synchronously, dropping spinner ticks so animations do not loop forever.
type driver struct {
	m    *Model
	quit bool
}

func (d *driver) send(msgs ...tea.Msg) {
	queue := msgs
	for len(queue) > 0 {
		msg := queue[0]
		queue = queue[1:]
		_, cmd := d.m.Update(msg)
		queue = append(queue, d.run(cmd)...)
	}
}

func (d *driver) run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case nil, spinner.TickMsg:
		return nil
	case tea.QuitMsg:
		d.quit = true
		return nil
	case tea.BatchMsg:
		var out []tea.Msg
		for _, c := range msg {
			out = append(out, d.run(c)...)
		}
		return out
	default:
		return []tea.Msg{msg}
	}
}

func (d *driver) keys(keys ...string) {
	for _, k := range keys {
		switch k {
		case "enter":
			d.send(tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			d.send(tea.KeyMsg{Type: tea.KeyEsc})
		case "down":
			d.send(tea.KeyMsg{Type: tea.KeyDown})
		case "up":
			d.send(tea.KeyMsg{Type: tea.KeyUp})
		case "tab":
			d.send(tea.KeyMsg{Type: tea.KeyTab})
		case "space":
			d.send(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		default:
			d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		}
	}
}

func (d *driver) view() string { return d.m.View() }

var _ = BeforeSuite(func() { cursorMode = cursor.CursorStatic })

// absDir is an absolute path on every platform.
var absDir, _ = filepath.Abs(filepath.Join(string(filepath.Separator), "abs", "x"))

var _ = Describe("TUI", func() {
	var (
		d       *driver
		a       *app.App
		project string
		origin  string
		repo    *git.Repository
	)

	BeforeEach(func() {
		base := GinkgoT().TempDir()
		GinkgoT().Setenv(config.EnvConfigDir, filepath.Join(base, "config"))
		GinkgoT().Setenv(config.EnvCacheDir, filepath.Join(base, "cache"))
		GinkgoT().Setenv("HOME", filepath.Join(base, "home"))
		GinkgoT().Setenv("USERPROFILE", filepath.Join(base, "home"))

		project = filepath.Join(base, "project")
		origin = filepath.Join(base, "origin")
		var err error
		repo, err = testutil.InitRepo(origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned skill", "1.0.0"),
			"skills/beta/SKILL.md":  testutil.SkillMD("beta", "Unversioned skill", ""),
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(testutil.WriteFiles(project, map[string]string{"README.md": "x"})).To(Succeed())

		a, err = app.New()
		Expect(err).NotTo(HaveOccurred())
		t := install.Target{Scope: agent.ScopeProject, ProjectRoot: project, Home: a.Home}
		d = &driver{m: newModel(context.Background(), a, "test", t)}
		d.send(d.m.Init())
	})

	addRepo := func() {
		d.keys("down", "down", "enter") // Add repository
		Expect(d.view()).To(ContainSubstring("Add a skill repository"))
		d.keys(origin, "enter")
		Expect(d.view()).To(ContainSubstring("alpha"))
		Expect(d.view()).To(ContainSubstring("beta"))
	}

	lockfile := func() *lock.File {
		lf, err := lock.Load(filepath.Join(project, lock.FileName))
		Expect(err).NotTo(HaveOccurred())
		return lf
	}

	It("shows the main menu and the current target", func() {
		v := d.view()
		Expect(v).To(ContainSubstring("Browse repositories"))
		Expect(v).To(ContainSubstring("project " + project))
	})

	It("lists the builtin repositories", func() {
		d.keys("enter")
		Expect(d.view()).To(ContainSubstring("anthropics/skills"))
		d.keys("esc")
		Expect(d.view()).To(ContainSubstring("Browse repositories"))
	})

	It("indexes newly added repositories when searching", func() {
		Expect(testutil.SeedIndex(a.Config, os.Getenv(config.EnvCacheDir))).To(Succeed())
		addRepo()
		d.keys("esc", "esc", "up", "enter") // Search skills
		Expect(d.view()).To(ContainSubstring("search:"))
		d.keys("alpha")
		v := d.view()
		Expect(v).To(ContainSubstring("Versioned skill"))
		Expect(v).NotTo(ContainSubstring("indexing repositories"))
	})

	It("shows indexing progress above the results", func() {
		s := newSearchScreen(d.m.env)
		s.idx = registry.NewIndex()
		s.ist = registry.IndexStatus{Building: true, Done: 3, Total: 19}
		Expect(s.View()).To(ContainSubstring("indexing repositories 3/19"))
		s.ist.Building = false
		Expect(s.View()).NotTo(ContainSubstring("indexing repositories"))
	})

	It("adds a repository, installs with version tracking and updates it", func() {
		addRepo()
		Expect(a.Config.RepoList()).To(HaveLen(1))

		d.keys("enter") // install alpha (cursor on first skill)
		Expect(d.view()).To(ContainSubstring("Where should skills be installed?"))
		d.keys("enter") // Project
		Expect(d.view()).To(ContainSubstring("project directory"))
		d.keys("enter") // keep prefilled directory
		Expect(d.view()).To(ContainSubstring("Which agents"))
		Expect(d.view()).To(ContainSubstring("[x] Claude Code"))
		d.keys("enter")
		Expect(d.view()).To(ContainSubstring("Track version"))
		d.keys("enter", "enter")
		Expect(d.view()).To(ContainSubstring("✓ alpha"))
		d.keys("enter")

		Expect(filepath.Join(project, ".claude", "skills", "alpha", "SKILL.md")).To(BeAnExistingFile())
		e, ok := lockfile().Get("alpha")
		Expect(ok).To(BeTrue())
		Expect(e.Tracking).To(Equal(lock.TrackVersion))
		Expect(e.Version).To(Equal("1.0.0"))

		Expect(testutil.Commit(repo, origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned skill", "1.1.0"),
		}, "bump")).To(Succeed())

		d.keys("esc", "esc") // back to menu
		Expect(d.view()).To(ContainSubstring("Browse repositories"))
		d.keys("down", "down", "enter") // Check for updates (cursor stays on "Add repository")
		Expect(d.view()).To(ContainSubstring("1.0.0 → 1.1.0"))
		d.keys("enter")
		Expect(d.view()).To(ContainSubstring("updated 1 skill(s)"))
		e, _ = lockfile().Get("alpha")
		Expect(e.Version).To(Equal("1.1.0"))
	})

	It("installs several skills globally with hash tracking for unversioned ones", func() {
		addRepo()
		d.keys("a", "enter")    // select all, install
		d.keys("down", "enter") // Global
		d.keys("enter")         // agents (default claude-code)
		Expect(d.view()).To(ContainSubstring("Version, otherwise hash"))
		d.keys("enter", "enter") // policy + confirm
		Expect(d.view()).To(ContainSubstring("✓ beta"))

		lf, err := lock.Load(filepath.Join(a.ConfigDir, lock.GlobalFileName))
		Expect(err).NotTo(HaveOccurred())
		beta, ok := lf.Get("beta")
		Expect(ok).To(BeTrue())
		Expect(beta.Tracking).To(Equal(lock.TrackHash))
		Expect(beta.Hash).To(HavePrefix("sha256:"))
		Expect(filepath.Join(a.Home, ".claude", "skills", "beta", "SKILL.md")).To(BeAnExistingFile())
	})

	It("shows and removes installed skills", func() {
		addRepo()
		d.keys("down", "enter", "enter", "enter", "enter") // beta → project → dir → agents
		d.keys("enter", "enter")                           // tracking (hash) + confirm
		Expect(d.view()).To(ContainSubstring("✓ beta"))
		d.keys("enter", "esc", "esc")

		d.keys("down", "enter") // Installed skills (cursor stays on "Add repository")
		// managed rows have no checkbox column
		Expect(d.view()).To(ContainSubstring("› beta hash "))
		d.keys("d")
		Expect(d.view()).To(ContainSubstring("Remove beta"))
		d.keys("y")
		Expect(d.view()).To(ContainSubstring("removed beta"))
		Expect(filepath.Join(project, ".claude", "skills", "beta")).NotTo(BeAnExistingFile())
	})

	It("shows and removes skills that were not installed by faehigkeiten", func() {
		Expect(testutil.WriteFiles(project, map[string]string{
			".claude/skills/handmade/SKILL.md": testutil.SkillMD("handmade", "Copied manually", ""),
		})).To(Succeed())
		d.keys("down", "down", "down", "enter") // Installed skills
		v := d.view()
		Expect(v).To(ContainSubstring("handmade"))
		Expect(v).To(ContainSubstring("not managed"))
		d.keys("d")
		Expect(d.view()).To(ContainSubstring("not installed by faehigkeiten"))
		d.keys("n")
		Expect(filepath.Join(project, ".claude", "skills", "handmade")).To(BeADirectory())
		d.keys("d", "y")
		Expect(d.view()).To(ContainSubstring("removed handmade"))
		Expect(filepath.Join(project, ".claude", "skills", "handmade")).NotTo(BeAnExistingFile())
	})

	Describe("managing unmanaged skills", func() {
		BeforeEach(func() {
			Expect(testutil.SeedIndex(a.Config, os.Getenv(config.EnvCacheDir), registry.Repo{Name: "fixture/skills", URL: origin})).To(Succeed())
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned skill", "1.0.0"),
				".claude/skills/beta/SKILL.md":  testutil.SkillMD("beta", "changed by hand", ""),
			})).To(Succeed())
			d.keys("down", "down", "down", "enter") // Installed skills
			Expect(d.view()).To(ContainSubstring("m manage"))
		})

		It("adopts from an identical source found in the known repositories", func() {
			d.keys("m")
			v := d.view()
			Expect(v).To(ContainSubstring("Where does this skill come from?"))
			Expect(v).To(ContainSubstring("fixture/skills"))
			Expect(v).To(ContainSubstring("identical"))
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("Your copy is identical to upstream"))
			d.keys("enter") // Track version
			Expect(d.view()).To(ContainSubstring("alpha is now managed from origin (Track version)"))
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("v1.0.0"))

			e, ok := lockfile().Get("alpha")
			Expect(ok).To(BeTrue())
			Expect(e.Path).To(Equal("skills/alpha"))
			Expect(e.Version).To(Equal("1.0.0"))
		})

		It("adopts from a repository entered by hand and keeps local changes", func() {
			d.keys("down", "m") // beta
			Expect(d.view()).To(ContainSubstring("differs from local copy"))
			d.keys("down", "enter") // Other repository…
			d.keys(origin, "enter")
			Expect(d.view()).To(ContainSubstring("Which skill in origin is it?"))
			d.keys("enter") // cursor is on beta
			Expect(d.view()).To(ContainSubstring("Your copy differs from upstream"))
			d.keys("enter") // Track content hash
			Expect(d.view()).To(ContainSubstring("beta is now managed"))

			e, _ := lockfile().Get("beta")
			Expect(e.Tracking).To(Equal(lock.TrackHash))
			data, err := os.ReadFile(filepath.Join(project, ".claude", "skills", "beta", "SKILL.md"))
			Expect(err).NotTo(HaveOccurred())
			Expect(string(data)).To(ContainSubstring("changed by hand"))
		})

		It("looks up sources while repositories are added or removed", func() {
			in, err := a.Installer(d.m.env.target)
			Expect(err).NotTo(HaveOccurred())
			f, err := app.FindUnmanaged(in, "alpha")
			Expect(err).NotTo(HaveOccurred())

			// Run each lookup like Bubble Tea would, on its own goroutine,
			// while this goroutine changes the configuration as the UI
			// does. Under -race this fails if a lookup reads Config.
			lookup := func(s screen) tea.Msg {
				cmds := tea.BatchMsg{s.Init()}
				if b, ok := cmds[0]().(tea.BatchMsg); ok {
					cmds = b
				}
				// Run every command at once so the spinner tick does not
				// delay the lookup; the routed message is its result.
				got := make(chan tea.Msg, len(cmds))
				for _, c := range cmds {
					if c == nil {
						continue
					}
					go func() {
						defer GinkgoRecover()
						if r, ok := c().(routed); ok {
							got <- r.msg
						}
					}()
				}
				// A missing local repository, so an index build that sees it
				// fails at once instead of going to the network.
				other := filepath.Join(GinkgoT().TempDir(), "missing")
				timeout := time.After(time.Minute)
				tick := time.NewTicker(time.Millisecond)
				defer tick.Stop()
				for {
					select {
					case msg := <-got:
						return msg
					case <-timeout:
						Fail("the lookup did not finish")
					case <-tick.C:
						a.Config.AddRepo(config.Repo{URL: other})
						a.Config.RemoveRepo(other)
					}
				}
			}

			msg := lookup(newAdoptScreen(d.m.env, in, f))
			Expect(msg).To(BeAssignableToTypeOf(candidatesMsg{}))
			cands := msg.(candidatesMsg)
			Expect(cands.err).NotTo(HaveOccurred())
			Expect(cands.cands).NotTo(BeEmpty())
			Expect(cands.cands[0].Entry.Repo.Name).To(Equal("fixture/skills"))

			msg = lookup(newAdoptManyScreen(d.m.env, in, []install.Found{f}))
			Expect(msg).To(BeAssignableToTypeOf(plansMsg{}))
			plans := msg.(plansMsg)
			Expect(plans.plans).To(HaveLen(1))
			Expect(plans.plans[0].resolved).To(BeTrue(), plans.plans[0].reason)
		})

		It("adopts several selected skills at once, choosing only identical sources", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/mine/SKILL.md": testutil.SkillMD("mine", "", ""),
			})).To(Succeed())
			d.keys("esc", "enter") // reopen Installed to pick up mine
			d.keys("a")            // select all unmanaged skills
			Expect(d.view()).To(ContainSubstring("3 selected"))
			d.keys("m")
			v := d.view()
			Expect(v).To(ContainSubstring("from origin • Track version"))
			Expect(v).To(ContainSubstring("beta differs from its only source"))
			Expect(v).To(ContainSubstring("no known repository contains a skill named mine"))
			d.keys("l")
			v = d.view()
			Expect(v).To(ContainSubstring("alpha is now managed from origin (Track version)"))
			Expect(v).To(ContainSubstring("beta is now managed (local only)"))
			Expect(v).To(ContainSubstring("mine is now managed (local only)"))
			d.keys("enter")
			Expect(d.view()).NotTo(ContainSubstring("not managed"))

			lf := lockfile()
			alpha, _ := lf.Get("alpha")
			Expect(alpha.Tracking).To(Equal(lock.TrackVersion))
			beta, ok := lf.Get("beta")
			Expect(ok).To(BeTrue())
			Expect(beta.Source).To(BeEmpty())
		})

		It("walks through the selected skills that need a source and can stop", func() {
			Expect(testutil.WriteFiles(project, map[string]string{
				".claude/skills/mine/SKILL.md": testutil.SkillMD("mine", "", ""),
			})).To(Succeed())
			d.keys("esc", "enter")
			d.keys("a", "m") // beta and mine need a source
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("2 skill(s) still need a source"))
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("Manage beta"))
			d.keys("esc") // back out: the walk stops
			v := d.view()
			Expect(v).To(ContainSubstring("alpha is now managed"))
			Expect(v).To(ContainSubstring("2 skill(s) still need a source"))
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("Manage mine")) // beta moved to the end
			d.keys("down", "enter", "enter")                     // mine: keep local only
			Expect(d.view()).To(ContainSubstring("Manage beta"))
			d.keys("down", "down", "enter", "enter") // beta: keep local only
			v = d.view()
			Expect(v).To(ContainSubstring("beta is now managed (local only)"))
			Expect(v).To(ContainSubstring("mine is now managed (local only)"))
			Expect(v).NotTo(ContainSubstring("still need a source"))
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("lockfile:"))
			for _, n := range []string{"alpha", "beta", "mine"} {
				_, ok := lockfile().Get(n)
				Expect(ok).To(BeTrue(), n)
			}
		})

		It("keeps the selection when the bulk screen is cancelled", func() {
			d.keys("a", "m", "esc")
			Expect(d.view()).To(ContainSubstring("2 selected"))
		})

		It("deletes the selected skills", func() {
			d.keys("a", "d")
			Expect(d.view()).To(ContainSubstring("Delete the 2 selected skills?"))
			d.keys("y")
			Expect(d.view()).To(ContainSubstring("removed 2 skills"))
			Expect(filepath.Join(project, ".claude", "skills", "alpha")).NotTo(BeAnExistingFile())
			Expect(filepath.Join(project, ".claude", "skills", "beta")).NotTo(BeAnExistingFile())
		})

		It("notes selections hidden by the filter", func() {
			d.keys("space", "/", "beta", "enter")
			Expect(d.view()).To(ContainSubstring("1 selected (1 hidden by the filter)"))
		})

		It("keeps a skill local only", func() {
			d.keys("m", "down", "down", "enter")
			Expect(d.view()).To(ContainSubstring("alpha is now managed (local only)"))
			e, _ := lockfile().Get("alpha")
			Expect(e.Source).To(BeEmpty())
		})
	})

	It("warns about updates that would overwrite local changes and does not select them", func() {
		in, err := a.Installer(d.m.env.target)
		Expect(err).NotTo(HaveOccurred())
		co, skills, err := a.Load(context.Background(), origin, "", false)
		Expect(err).NotTo(HaveOccurred())
		claude, _ := a.Agents.Get("claude-code")
		_, err = in.Install(install.Request{Skill: skills[0], Checkout: co, Agents: []agent.Agent{claude}})
		Expect(err).NotTo(HaveOccurred())
		Expect(in.Lock.Save()).To(Succeed())

		notes := filepath.Join(project, ".claude", "skills", "alpha", "NOTES.md")
		Expect(os.WriteFile(notes, []byte("mine"), 0o644)).To(Succeed())
		Expect(testutil.Commit(repo, origin, map[string]string{
			"skills/alpha/SKILL.md": testutil.SkillMD("alpha", "Versioned skill", "1.1.0"),
		}, "bump")).To(Succeed())

		d.keys("down", "down", "down", "down", "enter") // Check for updates
		v := d.view()
		Expect(v).To(ContainSubstring("Warning: 1 skill(s) with updates were modified locally"))
		Expect(v).To(ContainSubstring("[ ] alpha"))
		Expect(v).To(ContainSubstring("locally modified"))
		d.keys("enter") // nothing selected: nothing happens
		Expect(notes).To(BeAnExistingFile())

		d.keys("space", "enter") // explicitly selected: overwritten
		Expect(d.view()).To(ContainSubstring("updated 1 skill(s)"))
		Expect(notes).NotTo(BeAnExistingFile())
	})

	It("cancels the wizard when the target picker is closed", func() {
		addRepo()
		d.keys("enter", "esc")
		Expect(d.view()).To(ContainSubstring("space select"))
	})

	It("filters lists", func() {
		addRepo()
		d.keys("/", "bet")
		v := d.view()
		Expect(v).To(ContainSubstring("beta"))
		Expect(strings.Count(v, "alpha")).To(BeZero())
	})

	Describe("settings", func() {
		saved := func() *config.Config {
			cfg, err := config.Load()
			Expect(err).NotTo(HaveOccurred())
			return cfg
		}

		BeforeEach(func() {
			d.keys("G", "up", "enter") // Settings
			Expect(d.view()).To(ContainSubstring("detect agents in the target"))
		})

		It("sets and clears the default agents", func() {
			d.keys("enter", "down", "space", "enter") // Default agents: Universal (.agents)
			Expect(saved().DefaultAgentList()).To(Equal([]string{"agents"}))
			Expect(a.DefaultAgents(install.Target{Scope: agent.ScopeProject, ProjectRoot: project})).To(Equal([]string{"agents"}))
			Expect(d.view()).To(ContainSubstring("Preselected when installing: agents"))

			d.keys("enter", "c", "enter")
			Expect(saved().DefaultAgentList()).To(BeEmpty())
		})

		It("overrides builtin agents and resets them", func() {
			d.keys("down", "enter")                           // Agents
			d.keys("enter", "tab", ".claude/custom", "enter") // Claude Code: project dir
			Expect(saved().AgentOverrides()).To(HaveKeyWithValue("claude-code", config.AgentOverride{ProjectDir: ".claude/custom"}))
			c, _ := a.Agents.Get("claude-code")
			Expect(c.ProjectDir).To(Equal(".claude/custom"))
			Expect(d.view()).To(ContainSubstring("override"))

			d.keys("d")
			Expect(saved().AgentOverrides()).To(BeEmpty())
			c, _ = a.Agents.Get("claude-code")
			Expect(c.ProjectDir).To(Equal(".claude/skills"))
		})

		It("adds and deletes custom agents", func() {
			d.keys("down", "enter", "n") // Agents → new
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("the id is required"))
			d.keys("claude-code", "enter")
			Expect(d.view()).To(ContainSubstring("already exists"))
			d.send(tea.KeyMsg{Type: tea.KeyCtrlU})
			d.keys("mine", "enter")
			Expect(d.view()).To(ContainSubstring("set a project or global directory"))
			d.keys("tab", "Mine", "tab", ".mine/skills", "enter")
			m, ok := a.Agents.Get("mine")
			Expect(ok).To(BeTrue())
			Expect(m.Name).To(Equal("Mine"))
			Expect(d.view()).To(ContainSubstring("custom"))

			d.keys("esc", "up", "enter", "/", "Mine", "enter", "space", "enter") // Default agents: mine
			Expect(saved().DefaultAgentList()).To(Equal([]string{"mine"}))
			d.keys("down", "enter", "/", "Mine", "enter", "d")
			Expect(d.view()).To(ContainSubstring("deleted mine"))
			_, ok = a.Agents.Get("mine")
			Expect(ok).To(BeFalse())
			Expect(saved().AgentOverrides()).To(BeEmpty())
			Expect(saved().DefaultAgentList()).To(BeEmpty())
		})

		It("derives the name of an edited repository from its new source", func() {
			d.keys("down", "down", "enter", "n", "acme/one", "enter") // Repositories
			d.keys("enter")
			d.send(tea.KeyMsg{Type: tea.KeyCtrlU})
			d.keys("acme/two", "enter")
			Expect(saved().RepoList()).To(Equal([]config.Repo{{Name: "acme/two", URL: "https://github.com/acme/two"}}))
		})

		It("reports a repository removed while it is edited", func() {
			d.keys("down", "down", "enter", "n", "acme/one", "enter", "enter")
			cfg := saved() // removed outside the TUI
			cfg.RemoveRepo("https://github.com/acme/one")
			Expect(cfg.Save()).To(Succeed())
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("no longer configured"))
		})

		It("keeps the configuration unchanged when it cannot be saved", func() {
			blocker := filepath.Join(GinkgoT().TempDir(), "file")
			Expect(os.WriteFile(blocker, nil, 0o644)).To(Succeed())
			GinkgoT().Setenv(config.EnvConfigDir, filepath.Join(blocker, "config"))

			d.keys("enter", "space", "enter") // Default agents: Claude Code
			Expect(d.view()).To(ContainSubstring("Agents preselected when installing"), "stays on the screen")
			Expect(a.Config.DefaultAgentList()).To(BeEmpty())

			d.keys("esc", "down", "enter", "enter", "tab", ".x", "enter") // Agents: Claude Code
			Expect(a.Config.AgentOverrides()).To(BeEmpty())
			c, _ := a.Agents.Get("claude-code")
			Expect(c.ProjectDir).To(Equal(".claude/skills"))
		})

		It("drops default agents that are not known unless they are checked", func() {
			a.Config.SetDefaultAgents([]string{"ghost", "claude-code"})
			d.keys("enter")
			Expect(d.view()).To(ContainSubstring("not a known agent"))
			d.keys("enter")
			Expect(saved().DefaultAgentList()).To(Equal([]string{"claude-code"}))

			a.Config.SetDefaultAgents([]string{"ghost"})
			d.keys("enter", "G", "space", "enter")
			Expect(saved().DefaultAgentList()).To(Equal([]string{"ghost"}))
		})

		It("clears only the agents the filter shows", func() {
			d.keys("enter", "a", "/", "cursor", "enter", "c", "enter")
			ids := saved().DefaultAgentList()
			Expect(ids).To(ContainElement("claude-code"))
			Expect(ids).NotTo(ContainElement("cursor"))
		})

		It("keeps changes made to the file outside the TUI", func() {
			cfg := saved()
			cfg.AddRepo(config.Repo{Name: "outside", URL: "https://github.com/acme/outside"})
			Expect(cfg.Save()).To(Succeed())

			d.keys("enter", "space", "enter") // Default agents: Claude Code
			Expect(saved().RepoList()).To(HaveLen(1))
			Expect(saved().DefaultAgentList()).To(Equal([]string{"claude-code"}))
			Expect(a.Config.RepoList()).To(HaveLen(1), "the running app sees it too")
		})

		DescribeTable("checks agent directories",
			func(project, global, errText string) {
				d.keys("down", "enter", "n", "mine", "tab", "tab", project, "tab", global, "enter")
				if errText != "" {
					Expect(d.view()).To(ContainSubstring(errText))
					Expect(saved().AgentOverrides()).To(BeEmpty())
					return
				}
				Expect(saved().AgentOverrides()).To(HaveKey("mine"))
			},
			Entry("outside the project", "../x", "", "must be a path inside the project"),
			Entry("absolute project dir", absDir, "", "must be a path inside the project"),
			Entry("home shorthand in project", "~/x", "", "must be a path inside the project"),
			Entry("outside home", "", "../x", "relative to your home directory"),
			Entry("absolute global dir", "", absDir, ""),
			Entry("relative dirs", ".mine/skills", ".mine/skills", ""),
		)

		It("drops a leading ~/ from global directories", func() {
			d.keys("down", "enter", "n", "mine", "tab", "tab", "tab", "~/.mine/skills", "enter")
			Expect(saved().AgentOverrides()).To(HaveKeyWithValue("mine", config.AgentOverride{GlobalDir: ".mine/skills"}))
		})

		It("rejects agent IDs that cannot be used in agent lists", func() {
			d.keys("down", "enter", "n", "my,agent", "enter")
			Expect(d.view()).To(ContainSubstring("may only contain"))
		})

		It("warns before changing or deleting agents that have installed skills", func() {
			lf := lockfile()
			lf.Upsert(lock.Entry{Name: "alpha", Source: origin, Path: "skills/alpha", Agents: []string{"claude-code", "mine"}, Tracking: lock.TrackNone})
			Expect(lf.Save()).To(Succeed())

			d.keys("down", "enter", "enter") // Agents: Claude Code
			Expect(d.view()).To(ContainSubstring("Skills installed for claude-code (alpha)"))
			d.keys("tab", ".claude/custom", "enter")
			d.keys("d")
			Expect(d.view()).To(ContainSubstring("Press d again"))
			Expect(saved().AgentOverrides()).To(HaveKey("claude-code"))
			d.keys("d")
			Expect(saved().AgentOverrides()).To(BeEmpty())

			d.keys("n", "mine", "tab", "tab", ".mine", "enter", "/", "mine", "enter", "d")
			Expect(d.view()).To(ContainSubstring("can no longer be updated"))
			d.keys("down", "d") // moving on cancels
			Expect(saved().AgentOverrides()).To(HaveKey("mine"))
			d.keys("d", "d")
			Expect(saved().AgentOverrides()).To(BeEmpty())
		})

		It("adds, edits and removes repositories", func() {
			d.keys("down", "down", "enter") // Repositories
			Expect(d.view()).To(ContainSubstring("no repositories yet"))
			d.keys("n", origin, "tab", "dev", "enter")
			Expect(saved().RepoList()).To(Equal([]config.Repo{{Name: "origin", URL: origin, Ref: "dev"}}))
			Expect(d.view()).To(ContainSubstring(origin + " @ dev"))

			d.keys("enter", "tab", "tab", "Mine", "enter")
			Expect(saved().RepoList()).To(Equal([]config.Repo{{Name: "Mine", URL: origin, Ref: "dev"}}))

			d.keys("n", origin, "enter")
			Expect(d.view()).To(ContainSubstring("already configured"))
			d.keys("esc", "d")
			Expect(saved().RepoList()).To(BeEmpty())
			Expect(a.Config.RepoList()).To(BeEmpty())
		})
	})

	DescribeTable("fills the whole terminal",
		func(width, height int, footer string, keys ...string) {
			d.send(tea.WindowSizeMsg{Width: width, Height: height})
			d.keys(keys...)
			lines := strings.Split(d.view(), "\n")
			Expect(lines).To(HaveLen(height))
			for _, l := range lines {
				Expect(lipgloss.Width(l)).To(BeNumerically("<=", width), l)
			}
			Expect(lipgloss.Width(lines[0])).To(Equal(width), "title bar spans the width")
			Expect(lines[height-1]).To(ContainSubstring(footer), "help bar is pinned to the bottom")
		},
		Entry("menu, large terminal", 140, 50, "enter select • q quit"),
		Entry("menu, small terminal", 60, 15, "q quit"),
		Entry("long repository list is clipped", 80, 20, "enter open", "enter"),
		Entry("tiny terminal", 30, 8, "enter open", "enter"),
		Entry("settings, tiny terminal", 30, 8, "enter select", "G", "up", "enter"),
		Entry("agents, small terminal", 60, 12, "enter edit", "G", "up", "enter", "down", "enter"),
	)

	It("quits from the menu", func() {
		d.keys("q")
		Expect(d.quit).To(BeTrue())
	})
})
