package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"

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

	It("adds a repository, installs with version tracking and updates it", func() {
		addRepo()
		Expect(a.Config.Repos).To(HaveLen(1))

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
		Expect(d.view()).To(ContainSubstring("beta"))
		Expect(d.view()).To(ContainSubstring("hash "))
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
			Expect(testutil.SeedIndex(os.Getenv(config.EnvCacheDir), registry.Repo{Name: "fixture/skills", URL: origin})).To(Succeed())
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

		It("keeps a skill local only", func() {
			d.keys("m", "down", "down", "enter")
			Expect(d.view()).To(ContainSubstring("alpha is now managed (local only)"))
			e, _ := lockfile().Get("alpha")
			Expect(e.Source).To(BeEmpty())
		})
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
	)

	It("quits from the menu", func() {
		d.keys("q")
		Expect(d.quit).To(BeTrue())
	})
})
