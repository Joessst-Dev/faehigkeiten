package tui

import (
	"errors"
	"fmt"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// ---- license text of a skill ----

type licenseLoadedMsg struct {
	text, note string
	err        error
}

type licenseScreen struct {
	env     *env
	name    string
	load    func() (text, note string, err error)
	loading bool
	spin    spinner.Model
	view    viewport.Model
	text    string
	note    string // where the text comes from, when not the installed version
	width   int    // width text was wrapped for
	err     error
}

func newLicenseScreen(e *env, name string, load func() (text, note string, err error)) *licenseScreen {
	return &licenseScreen{env: e, name: name, load: load, spin: newSpinner(), view: viewport.New(0, 0)}
}

// licenseOfSkill shows the license of a skill in a checkout.
func licenseOfSkill(e *env, co *source.Checkout, s skill.Skill) *licenseScreen {
	return newLicenseScreen(e, s.Name, noNote(func() (string, error) { return app.LicenseText(co.Dir, s.License) }))
}

// licenseOfInstalled shows the license of a lockfile entry or unmanaged skill.
func licenseOfInstalled(e *env, in *install.Installer, v any) *licenseScreen {
	switch v := v.(type) {
	case lock.Entry:
		return newLicenseScreen(e, v.Name, func() (string, string, error) { return e.app.EntryLicenseText(e.ctx, in, v) })
	case install.Found:
		return newLicenseScreen(e, v.Name, noNote(func() (string, error) { return app.FoundLicenseText(v) }))
	}
	return nil
}

// licenseOfHit shows the license of a search result, fetching its repository.
func licenseOfHit(e *env, v any) *licenseScreen {
	var src, ref, sel, name string
	switch v := v.(type) {
	case registry.Entry:
		src, ref, sel, name = v.Repo.URL, v.Repo.Ref, v.Path, v.Name
	case registry.RemoteSkill:
		src, sel, name = "https://github.com/"+v.Source, v.Name, v.Name
	default:
		return nil
	}
	return newLicenseScreen(e, name, noNote(func() (string, error) { return e.app.SkillLicenseText(e.ctx, src, ref, sel) }))
}

// noNote adapts loaders of texts that always describe the shown version.
func noNote(load func() (string, error)) func() (string, string, error) {
	return func() (string, string, error) {
		text, err := load()
		return text, "", err
	}
}

func (s *licenseScreen) Title() string { return s.name + " license" }
func (s *licenseScreen) Help() string {
	if s.loading || s.err != nil {
		return ""
	}
	return "↑/↓ scroll • pgup/pgdown page"
}

func (s *licenseScreen) Init() tea.Cmd {
	s.loading = true
	load := s.load
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		text, note, err := load()
		return licenseLoadedMsg{text, note, err}
	}))
}

func (s *licenseScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return tick(&s.spin, s.loading, msg)
	case licenseLoadedMsg:
		s.loading, s.text, s.note, s.err = false, msg.text, msg.note, msg.err
		s.width = 0
		return nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			return pop
		}
		var cmd tea.Cmd
		s.view, cmd = s.view.Update(msg)
		return cmd
	}
	return nil
}

func (s *licenseScreen) View() string {
	if s.loading {
		return spinLine(s.spin, "loading license")
	}
	if errors.Is(s.err, app.ErrNoLicense) {
		return subtleStyle.Render(fmt.Sprintf("%s declares no license and has no license file.", s.name))
	}
	if s.err != nil {
		return errStyle.Render("Could not load the license: " + s.err.Error())
	}
	w := s.env.bodyWidth()
	head := ""
	if s.note != "" {
		head = warnStyle.Width(w).Render("Note: "+s.note) + "\n\n"
	}
	s.view.Width, s.view.Height = w, max(1, s.env.bodyHeight()-lipgloss.Height(head)+1)
	if s.width != w {
		s.width = w
		s.view.SetContent(lipgloss.NewStyle().Width(w).Render(s.text))
	}
	return head + s.view.View()
}
