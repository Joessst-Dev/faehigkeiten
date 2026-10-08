package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/registry"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// ---- skills of one repository ----

type skillsLoadedMsg struct {
	co     *source.Checkout
	skills []skill.Skill
	err    error
}

type skillsScreen struct {
	env      *env
	name     string
	src, ref string
	focus    string
	loading  bool
	spin     spinner.Model
	list     *pickList
	co       *source.Checkout
	err      error
}

// newSkillsScreen lists the skills of a source; focus optionally names a
// skill (by name or path) to put the cursor on.
func newSkillsScreen(e *env, name, src, ref, focus string) *skillsScreen {
	l := newPickList(nil, true)
	l.emptyMsg = "no SKILL.md found in this repository"
	return &skillsScreen{env: e, name: name, src: src, ref: ref, focus: focus, spin: newSpinner(), list: l}
}

func (s *skillsScreen) Title() string { return s.name }
func (s *skillsScreen) Help() string {
	if s.loading {
		return ""
	}
	return "space select • a all • enter install • / filter • r refresh"
}

func (s *skillsScreen) Init() tea.Cmd { return s.load(false) }

func (s *skillsScreen) load(refresh bool) tea.Cmd {
	s.loading, s.err = true, nil
	e := s.env
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		co, skills, err := e.app.Load(e.ctx, s.src, s.ref, refresh)
		return skillsLoadedMsg{co, skills, err}
	}))
}

func (s *skillsScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.loading {
			return nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return cmd
	case skillsLoadedMsg:
		s.loading = false
		if msg.err != nil {
			s.err = msg.err
			return nil
		}
		s.co = msg.co
		items := make([]pickItem, 0, len(msg.skills))
		for _, sk := range msg.skills {
			items = append(items, pickItem{Title: sk.Name, Desc: sk.Description, Badge: versionBadge(sk.Version), Value: sk})
		}
		s.list.SetItems(items)
		if s.focus != "" {
			s.list.Focus(func(it pickItem) bool {
				sk := it.Value.(skill.Skill)
				return strings.EqualFold(sk.Name, s.focus) || sk.Path == s.focus
			})
		}
		return nil
	case tea.KeyMsg:
		if s.loading {
			if msg.String() == "esc" {
				return pop
			}
			return nil
		}
		if handled, cmd := s.list.Update(msg); handled {
			return cmd
		}
		switch msg.String() {
		case "esc":
			return pop
		case "r":
			return s.load(true)
		case "enter":
			if s.co == nil {
				return nil
			}
			var picked []skill.Skill
			for _, it := range s.list.Checked() {
				picked = append(picked, it.Value.(skill.Skill))
			}
			if len(picked) == 0 {
				it, ok := s.list.Current()
				if !ok {
					return nil
				}
				picked = []skill.Skill{it.Value.(skill.Skill)}
			}
			return push(newWizard(s.env, s.co, picked))
		}
	}
	return nil
}

func versionBadge(v string) string {
	if v == "" {
		return "no version"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func (s *skillsScreen) View() string {
	if s.loading {
		return s.spin.View() + " fetching " + s.src + " …"
	}
	if s.err != nil {
		return errStyle.Render("Could not load repository: "+s.err.Error()) + "\n\n" + subtleStyle.Render("press r to retry")
	}
	head := subtleStyle.Render(fmt.Sprintf("%s @ %s • %d skills", s.co.URL, shortCommit(s.co), len(s.list.items)))
	return head + "\n\n" + s.list.View(s.env.width, s.env.bodyHeight()-2)
}

func shortCommit(co *source.Checkout) string {
	if co.Commit == "" {
		return "local"
	}
	ref := co.Ref
	if ref == "" {
		ref = "HEAD"
	}
	return ref + " " + co.Commit[:min(7, len(co.Commit))]
}

// ---- search ----

type (
	indexLoadedMsg struct {
		idx *registry.Index
		err error
	}
	remoteResultsMsg struct {
		query string
		hits  []registry.RemoteSkill
		err   error
	}
)

type searchScreen struct {
	env     *env
	input   textinput.Model
	list    *pickList
	spin    spinner.Model
	loading bool
	idx     *registry.Index
	remote  bool
	lastQ   string
	err     error
}

func newSearchScreen(e *env) *searchScreen {
	ti := newInput()
	ti.Prompt = "search: "
	ti.Placeholder = "e.g. pdf, golang testing, terraform"
	ti.Width = 50
	l := newPickList(nil, false)
	l.emptyMsg = "no matches"
	return &searchScreen{env: e, input: ti, list: l, spin: newSpinner()}
}

func (s *searchScreen) Title() string { return "Search" }
func (s *searchScreen) Help() string {
	mode := "tab search skills.sh"
	if s.remote {
		mode = "tab search known repositories"
	}
	return "↑/↓ move • enter open • " + mode + " • ctrl+r refresh index"
}

func (s *searchScreen) Init() tea.Cmd {
	return tea.Batch(s.input.Focus(), s.loadIndex(false))
}

func (s *searchScreen) loadIndex(refresh bool) tea.Cmd {
	s.loading, s.err = true, nil
	e := s.env
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		idx, err := e.app.Index(e.ctx, refresh, nil)
		return indexLoadedMsg{idx, err}
	}))
}

func (s *searchScreen) searchRemote() tea.Cmd {
	q := strings.TrimSpace(s.input.Value())
	if q == "" {
		return nil
	}
	s.lastQ, s.loading, s.err = q, true, nil
	e := s.env
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		hits, err := e.app.Remote.Search(e.ctx, q, 50)
		return remoteResultsMsg{q, hits, err}
	}))
}

func (s *searchScreen) refreshLocal() {
	if s.idx == nil {
		return
	}
	hits := s.idx.Search(s.input.Value())
	items := make([]pickItem, 0, min(len(hits), 200))
	for i, h := range hits {
		if i == 200 {
			break
		}
		items = append(items, pickItem{Title: h.Name, Desc: h.Repo.Name + " — " + h.Description, Badge: versionBadge(h.Version), Value: h})
	}
	s.list.SetItems(items)
}

func (s *searchScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if !s.loading {
			return nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return cmd
	case indexLoadedMsg:
		s.loading = false
		s.idx, s.err = msg.idx, msg.err
		if !s.remote {
			s.refreshLocal()
		}
		return nil
	case remoteResultsMsg:
		s.loading = false
		if !s.remote {
			return nil
		}
		if msg.err != nil {
			s.err = fmt.Errorf("skills.sh: %w", msg.err)
			s.list.SetItems(nil)
			return nil
		}
		items := make([]pickItem, 0, len(msg.hits))
		for _, h := range msg.hits {
			items = append(items, pickItem{Title: h.Name, Desc: h.Source, Badge: fmt.Sprintf("%s installs", humanCount(h.Installs)), Value: h})
		}
		s.list.SetItems(items)
		return nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			return pop
		case "up", "down", "pgup", "pgdown":
			_, cmd := s.list.Update(msg)
			return cmd
		case "tab":
			s.remote = !s.remote
			s.err = nil
			s.list.SetItems(nil)
			if s.remote {
				s.lastQ = ""
				return s.searchRemote()
			}
			s.refreshLocal()
			return nil
		case "ctrl+r":
			if s.remote {
				return s.searchRemote()
			}
			return s.loadIndex(true)
		case "enter":
			if s.remote && strings.TrimSpace(s.input.Value()) != s.lastQ {
				return s.searchRemote()
			}
			it, ok := s.list.Current()
			if !ok {
				return nil
			}
			switch v := it.Value.(type) {
			case registry.Entry:
				return push(newSkillsScreen(s.env, v.Repo.Name, v.Repo.URL, v.Repo.Ref, v.Path))
			case registry.RemoteSkill:
				return push(newSkillsScreen(s.env, v.Source, "https://github.com/"+v.Source, "", v.Name))
			}
			return nil
		}
		var cmd tea.Cmd
		s.input, cmd = s.input.Update(msg)
		if !s.remote {
			s.refreshLocal()
		}
		return cmd
	}
	return nil
}

func (s *searchScreen) View() string {
	mode := "known repositories"
	if s.remote {
		mode = "skills.sh (press enter to search)"
	}
	v := s.input.View() + "\n" + subtleStyle.Render("searching "+mode) + "\n\n"
	switch {
	case s.loading && s.idx == nil && !s.remote:
		return v + s.spin.View() + fmt.Sprintf(" indexing %d repositories (first run can take a minute) …", len(s.env.app.Repos()))
	case s.loading:
		v += s.spin.View() + " searching …\n"
	}
	if s.err != nil {
		v += errStyle.Render(s.err.Error()) + "\n"
	}
	if s.idx != nil && !s.remote && len(s.idx.Errors) > 0 {
		v += warnStyle.Render(fmt.Sprintf("%d repositories could not be indexed", len(s.idx.Errors))) + "\n"
	}
	return v + s.list.View(s.env.width, s.env.bodyHeight()-5)
}
