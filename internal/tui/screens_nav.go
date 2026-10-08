package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

func newSpinner() spinner.Model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = accentStyle
	return s
}

// ---- menu ----

type menuScreen struct {
	env  *env
	list *pickList
}

const (
	menuBrowse    = "browse"
	menuSearch    = "search"
	menuAdd       = "add"
	menuInstalled = "installed"
	menuUpdates   = "updates"
	menuTarget    = "target"
	menuQuit      = "quit"
)

func newMenu(e *env) *menuScreen {
	return &menuScreen{env: e, list: newPickList([]pickItem{
		{Title: "Browse repositories", Desc: "Pick skills from well-known and your own skill repositories", Value: menuBrowse},
		{Title: "Search skills", Desc: "Search all known repositories (or skills.sh) by name and description", Value: menuSearch},
		{Title: "Add repository", Desc: "Install from any GitHub repo, git URL or local directory", Value: menuAdd},
		{Title: "Installed skills", Desc: "Show and remove skills installed in the current target", Value: menuInstalled},
		{Title: "Check for updates", Desc: "Compare tracked skills by version or content hash", Value: menuUpdates},
		{Title: "Change target", Desc: "Switch between a project directory and the global scope", Value: menuTarget},
		{Title: "Quit", Value: menuQuit},
	}, false)}
}

func (s *menuScreen) Init() tea.Cmd { return nil }
func (s *menuScreen) Title() string { return "" }
func (s *menuScreen) Help() string  { return "↑/↓ move • enter select • q quit" }

func (s *menuScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "q", "esc":
		return tea.Quit
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		switch it.Value {
		case menuBrowse:
			return push(newReposScreen(s.env))
		case menuSearch:
			return push(newSearchScreen(s.env))
		case menuAdd:
			return push(newAddRepoScreen(s.env))
		case menuInstalled:
			return push(newInstalledScreen(s.env))
		case menuUpdates:
			return push(newUpdatesScreen(s.env))
		case menuTarget:
			return push(newTargetScreen(s.env))
		case menuQuit:
			return tea.Quit
		}
	}
	return nil
}

func (s *menuScreen) View() string { return s.list.View(s.env.bodyWidth(), s.env.bodyHeight()) }

// ---- target ----

type targetScreen struct {
	env      *env
	list     *pickList
	path     textinput.Model
	askPath  bool
	err      error
	onChange func(t targetChoice)
}

type targetChoice struct {
	scope agent.Scope
	root  string
}

func newTargetScreen(e *env) *targetScreen {
	return newTargetPicker(e, e.target.Scope, e.target.ProjectRoot, func(c targetChoice) {
		if t, err := e.app.Target(c.scope, c.root); err == nil {
			e.target = t
		}
	})
}

// newTargetPicker asks for a scope and, for project scope, a directory.
func newTargetPicker(e *env, scope agent.Scope, root string, onChange func(targetChoice)) *targetScreen {
	l := newPickList([]pickItem{
		{Title: "Project", Desc: "Install into a project directory (skills are committed with the project)", Value: agent.ScopeProject},
		{Title: "Global", Desc: "Install for your user, available in every project (" + e.app.Home + ")", Value: agent.ScopeGlobal},
	}, false)
	l.Focus(func(it pickItem) bool { return it.Value == scope })
	ti := newInput()
	ti.Prompt = "project directory: "
	ti.SetValue(root)
	ti.Width = 60
	return &targetScreen{env: e, list: l, path: ti, onChange: onChange}
}

func (s *targetScreen) Init() tea.Cmd { return nil }
func (s *targetScreen) Title() string { return "Target" }
func (s *targetScreen) Help() string {
	if s.askPath {
		return "enter confirm (git root is detected automatically)"
	}
	return "↑/↓ move • enter select"
}

func (s *targetScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if s.askPath {
		switch k.String() {
		case "esc":
			s.askPath, s.err = false, nil
			s.path.Blur()
			return nil
		case "enter":
			root, err := app.ProjectRoot(strings.TrimSpace(s.path.Value()))
			if err != nil {
				s.err = err
				return nil
			}
			s.onChange(targetChoice{scope: agent.ScopeProject, root: root})
			return pop
		}
		var cmd tea.Cmd
		s.path, cmd = s.path.Update(k)
		return cmd
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "enter":
		it, _ := s.list.Current()
		if it.Value == agent.ScopeGlobal {
			s.onChange(targetChoice{scope: agent.ScopeGlobal})
			return pop
		}
		s.askPath = true
		if s.path.Value() == "" {
			if root, err := app.ProjectRoot(""); err == nil {
				s.path.SetValue(root)
			}
		}
		s.path.CursorEnd()
		return s.path.Focus()
	}
	return nil
}

func (s *targetScreen) View() string {
	if !s.askPath {
		return "Where should skills be installed?\n\n" + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-2)
	}
	v := s.path.View()
	if s.err != nil {
		v += "\n\n" + errStyle.Render(s.err.Error())
	}
	return v
}

// ---- repositories ----

type reposScreen struct {
	env  *env
	list *pickList
	msg  string
}

func newReposScreen(e *env) *reposScreen {
	s := &reposScreen{env: e}
	s.list = newPickList(nil, false)
	s.reload()
	return s
}

func (s *reposScreen) reload() {
	var items []pickItem
	for _, r := range s.env.app.Repos() {
		badge := ""
		switch {
		case r.Custom:
			badge = "custom"
		case r.Stars > 0:
			badge = fmt.Sprintf("★ %s", humanCount(r.Stars))
		}
		desc := r.Description
		if desc == "" {
			desc = r.URL
		}
		items = append(items, pickItem{Title: r.Name, Desc: desc, Badge: badge, Value: r})
	}
	s.list.SetItems(items)
}

func humanCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func (s *reposScreen) Init() tea.Cmd   { return nil }
func (s *reposScreen) Resume() tea.Cmd { s.reload(); return nil }
func (s *reposScreen) Title() string   { return "Repositories" }
func (s *reposScreen) Help() string {
	return "enter open • / filter • n add repository • d remove custom"
}

func (s *reposScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "n":
		return push(newAddRepoScreen(s.env))
	case "d":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		r := it.Value.(registry.Repo)
		if !r.Custom {
			s.msg = "builtin repositories cannot be removed"
			return nil
		}
		s.env.app.Config.RemoveRepo(r.URL)
		if err := s.env.app.Config.Save(); err != nil {
			s.msg = err.Error()
			return nil
		}
		s.msg = "removed " + r.Name
		s.reload()
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		r := it.Value.(registry.Repo)
		return push(newSkillsScreen(s.env, r.Name, r.URL, r.Ref, ""))
	}
	return nil
}

func (s *reposScreen) View() string {
	v := s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-1)
	if s.msg != "" {
		v += "\n" + warnStyle.Render(s.msg)
	}
	return v
}

// ---- add repository ----

type addRepoScreen struct {
	env    *env
	inputs []textinput.Model
	focus  int
	err    error
}

func newAddRepoScreen(e *env) *addRepoScreen {
	src := newInput()
	src.Prompt = "source: "
	src.Placeholder = "owner/repo, https://…, git@…, or ./local/dir"
	src.Width = 60
	ref := newInput()
	ref.Prompt = "branch/tag (optional): "
	ref.Width = 30
	return &addRepoScreen{env: e, inputs: []textinput.Model{src, ref}}
}

func (s *addRepoScreen) Init() tea.Cmd { return s.inputs[0].Focus() }
func (s *addRepoScreen) Title() string { return "Add repository" }
func (s *addRepoScreen) Help() string  { return "tab next field • enter add & open" }

func (s *addRepoScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc":
		return pop
	case "tab", "shift+tab", "down", "up":
		s.inputs[s.focus].Blur()
		s.focus = (s.focus + 1) % len(s.inputs)
		return s.inputs[s.focus].Focus()
	case "enter":
		src, err := source.Normalize(s.inputs[0].Value())
		if err != nil {
			s.err = err
			return nil
		}
		ref := strings.TrimSpace(s.inputs[1].Value())
		cfg := s.env.app.Config
		name := source.DisplayName(src)
		if cfg.AddRepo(config.Repo{Name: name, URL: src, Ref: ref}) {
			if err := cfg.Save(); err != nil {
				s.err = err
				return nil
			}
		}
		return replace(newSkillsScreen(s.env, name, src, ref, ""))
	}
	var cmd tea.Cmd
	s.inputs[s.focus], cmd = s.inputs[s.focus].Update(k)
	return cmd
}

func (s *addRepoScreen) View() string {
	v := "Add a skill repository. It is saved to your configuration and shows up\nin the repository list.\n\n" +
		s.inputs[0].View() + "\n" + s.inputs[1].View()
	if s.err != nil {
		v += "\n\n" + errStyle.Render(s.err.Error())
	}
	return v
}
