// Package tui implements the interactive terminal UI.
package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
)

// screen is one view on the navigation stack. Screens are pointers and
// mutate themselves in Update.
type screen interface {
	Init() tea.Cmd
	Update(msg tea.Msg) tea.Cmd
	View() string
	Title() string
	Help() string
}

// resumer is implemented by screens that refresh when they become visible again.
type resumer interface {
	Resume() tea.Cmd
}

type (
	pushMsg    struct{ s screen }
	popMsg     struct{}
	replaceMsg struct{ s screen }
	// routed delivers an async result to the screen that started it.
	routed struct {
		to  screen
		msg tea.Msg
	}
)

// env is shared by all screens.
type env struct {
	app     *app.App
	ctx     context.Context
	target  install.Target
	width   int
	height  int
	version string
}

func push(s screen) tea.Cmd    { return func() tea.Msg { return pushMsg{s} } }
func replace(s screen) tea.Cmd { return func() tea.Msg { return replaceMsg{s} } }
func pop() tea.Msg             { return popMsg{} }

// async runs f in the background and routes its result to s.
func async(s screen, f func() tea.Msg) tea.Cmd {
	return func() tea.Msg { return routed{to: s, msg: f()} }
}

// Model is the root bubbletea model.
type Model struct {
	env   *env
	stack []screen
}

// New creates the root model with the default target (project at the
// working directory's git root, or global if that fails).
func New(ctx context.Context, a *app.App, version string) *Model {
	t, err := a.Target(agent.ScopeProject, "")
	if err != nil {
		t, _ = a.Target(agent.ScopeGlobal, "")
	}
	return newModel(ctx, a, version, t)
}

func newModel(ctx context.Context, a *app.App, version string, t install.Target) *Model {
	e := &env{app: a, ctx: ctx, version: version, target: t, width: 100, height: 40}
	return &Model{env: e, stack: []screen{newMenu(e)}}
}

// Run starts the TUI.
func Run(ctx context.Context, a *app.App, version string) error {
	p := tea.NewProgram(New(ctx, a, version), tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	return err
}

func (m *Model) top() screen { return m.stack[len(m.stack)-1] }

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd { return m.top().Init() }

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.env.width, m.env.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
	case pushMsg:
		m.stack = append(m.stack, msg.s)
		return m, msg.s.Init()
	case replaceMsg:
		m.stack[len(m.stack)-1] = msg.s
		return m, msg.s.Init()
	case popMsg:
		if len(m.stack) == 1 {
			return m, tea.Quit
		}
		m.stack = m.stack[:len(m.stack)-1]
		if r, ok := m.top().(resumer); ok {
			return m, r.Resume()
		}
		return m, nil
	case routed:
		for _, s := range m.stack {
			if s == msg.to {
				return m, s.Update(msg.msg)
			}
		}
		return m, nil // screen was closed meanwhile
	}
	return m, m.top().Update(msg)
}

var (
	accent    = lipgloss.AdaptiveColor{Light: "#5A3FC0", Dark: "#B39DFF"}
	subtle    = lipgloss.AdaptiveColor{Light: "#6B6B6B", Dark: "#8A8A8A"}
	okColor   = lipgloss.AdaptiveColor{Light: "#1E7D32", Dark: "#7CD992"}
	warnColor = lipgloss.AdaptiveColor{Light: "#B26A00", Dark: "#FFC66D"}
	errColor  = lipgloss.AdaptiveColor{Light: "#C62828", Dark: "#FF8A80"}

	barBg       = lipgloss.AdaptiveColor{Light: "#ECE8F8", Dark: "#2B2640"}
	barStyle    = lipgloss.NewStyle().Background(barBg).Foreground(subtle)
	footerStyle = lipgloss.NewStyle().Background(barBg).Foreground(subtle).Padding(0, 1)
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFFFFF")).Background(accent).Padding(0, 1)
	crumbStyle  = lipgloss.NewStyle().Background(barBg).Foreground(subtle)
	helpStyle   = lipgloss.NewStyle().Foreground(subtle)
	subtleStyle = lipgloss.NewStyle().Foreground(subtle)
	accentStyle = lipgloss.NewStyle().Foreground(accent).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(okColor)
	warnStyle   = lipgloss.NewStyle().Foreground(warnColor)
	errStyle    = lipgloss.NewStyle().Foreground(errColor)
)

// Layout: title bar, target line, blank, body, blank, help bar.
const chromeLines = 5

// View implements tea.Model. It always fills the whole terminal: the body is
// padded or clipped to the space between the title bar and the help bar.
func (m *Model) View() string {
	w := m.env.width
	var crumbs []string
	for _, s := range m.stack[1:] {
		crumbs = append(crumbs, s.Title())
	}
	left := titleStyle.Render("faehigkeiten") + " " + crumbStyle.Render(strings.Join(crumbs, " › "))
	right := barStyle.Render(m.env.version + " ")
	gap := max(0, w-lipgloss.Width(left)-lipgloss.Width(right))
	header := barStyle.MaxWidth(w).Render(left + barStyle.Render(strings.Repeat(" ", gap)) + right)

	target := subtleStyle.MaxWidth(w).Render(" target: " + describeTarget(m.env.target))

	help := "ctrl+c quit"
	if len(m.stack) > 1 {
		help = "esc back • " + help
	}
	if hl := m.top().Help(); strings.HasSuffix(hl, " quit") {
		help = hl // the screen already explains how to quit
	} else if hl != "" {
		help = hl + " • " + help
	}
	footer := footerStyle.Width(w).MaxWidth(w).MaxHeight(1).Render(help)

	body := lipgloss.NewStyle().
		Padding(0, 1).
		Width(w).MaxWidth(w).
		Height(m.env.bodyHeight()).MaxHeight(m.env.bodyHeight()).
		Render(m.top().View())

	return lipgloss.JoinVertical(lipgloss.Left, header, target, "", body, "", footer)
}

// bodyHeight is the number of lines available to a screen's content.
func (e *env) bodyHeight() int {
	return max(3, e.height-chromeLines)
}

// bodyWidth is the number of columns available to a screen's content.
func (e *env) bodyWidth() int {
	return max(20, e.width-2)
}

func describeTarget(t install.Target) string {
	if t.Scope == agent.ScopeGlobal {
		return "global (" + t.Home + ")"
	}
	return "project " + t.ProjectRoot
}
