package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// adoptScreen turns an unmanaged skill into a managed one by choosing the
// source it came from and how to track it. The skill's files are not changed.

type adoptStep int

const (
	adoptFinding adoptStep = iota
	adoptSource
	adoptOther
	adoptLoading
	adoptPickSkill
	adoptTracking
	adoptDone
)

type (
	candidatesMsg struct {
		cands []app.Candidate
		err   error
	}
	upstreamMsg struct {
		co     *source.Checkout
		up     *skill.Skill
		skills []skill.Skill
		err    error
	}
	adoptedMsg struct {
		entry lock.Entry
		err   error
	}
)

// choices on the source list besides real candidates
type (
	otherRepo struct{}
	localOnly struct{}
)

type adoptScreen struct {
	env    *env
	in     *install.Installer
	found  install.Found
	step   adoptStep
	spin   spinner.Model
	list   *pickList
	inputs []textinput.Model
	focus  int
	co     *source.Checkout
	up     *skill.Skill
	ident  bool
	result lock.Entry
	err    error
}

func newAdoptScreen(e *env, in *install.Installer, f install.Found) *adoptScreen {
	src := newInput()
	src.Prompt = "source: "
	src.Placeholder = "owner/repo, https://…, git@…, or ./local/dir"
	src.Width = 60
	ref := newInput()
	ref.Prompt = "branch/tag (optional): "
	ref.Width = 30
	return &adoptScreen{env: e, in: in, found: f, spin: newSpinner(), inputs: []textinput.Model{src, ref}}
}

func (s *adoptScreen) Title() string { return "Manage " + s.found.Name }

func (s *adoptScreen) Help() string {
	switch s.step {
	case adoptSource, adoptPickSkill, adoptTracking:
		return "↑/↓ move • enter select • / filter"
	case adoptOther:
		return "tab next field • enter load"
	case adoptDone:
		return "enter done"
	}
	return ""
}

func (s *adoptScreen) Init() tea.Cmd {
	s.step = adoptFinding
	e, f := s.env, s.found
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		cands, err := e.app.Candidates(e.ctx, f)
		return candidatesMsg{cands, err}
	}))
}

func (s *adoptScreen) sourceList(cands []app.Candidate) {
	var items []pickItem
	for _, c := range cands {
		badge := "differs from local copy"
		if c.Identical {
			badge = "identical"
		}
		desc := c.Entry.Path
		if c.Entry.Version != "" {
			desc += " • v" + strings.TrimPrefix(c.Entry.Version, "v")
		}
		items = append(items, pickItem{Title: c.Entry.Repo.Name, Badge: badge, Desc: desc, Value: c})
	}
	items = append(items,
		pickItem{Title: "Other repository…", Desc: "Enter the repository this skill came from", Value: otherRepo{}},
		pickItem{Title: "Keep local only", Desc: "Manage the skill without a source; it is never checked for updates", Value: localOnly{}},
	)
	s.list = newPickList(items, false)
}

func (s *adoptScreen) loadUpstream(src, ref, path string) tea.Cmd {
	s.step, s.err = adoptLoading, nil
	e, name := s.env, s.found.Name
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		if path == "" {
			// Unknown location: list the repository so the user can pick.
			co, skills, err := e.app.Load(e.ctx, src, ref, false)
			return upstreamMsg{co: co, skills: skills, err: err}
		}
		co, up, err := e.app.Upstream(e.ctx, src, ref, path, name)
		return upstreamMsg{co: co, up: up, err: err}
	}))
}

func (s *adoptScreen) chooseUpstream(co *source.Checkout, up *skill.Skill) {
	s.co, s.up = co, up
	if h, err := skill.Hash(up.Dir); err == nil {
		if l, err := skill.Hash(s.found.Dirs[0]); err == nil {
			s.ident = h == l
		}
	}
	var items []pickItem
	for _, t := range install.AvailableTracking(*up) {
		items = append(items, pickItem{Title: trackingLabel(t), Desc: adoptTrackingDesc(t, s.found, up), Value: t})
	}
	s.list = newPickList(items, false)
	s.step = adoptTracking
}

func adoptTrackingDesc(t lock.Tracking, f install.Found, up *skill.Skill) string {
	switch t {
	case lock.TrackVersion:
		local := f.Version
		if local == "" {
			local = "unknown"
		}
		return fmt.Sprintf("Compare versions (local %s, upstream %s)", local, up.Version)
	case lock.TrackHash:
		return "Store a hash of the local copy; updates are offered when upstream content differs"
	}
	return "Record the source but never check for updates"
}

func (s *adoptScreen) adopt(req install.AdoptRequest) tea.Cmd {
	s.step = adoptLoading
	in := s.in
	return async(s, func() tea.Msg {
		e, err := in.Adopt(req)
		if err == nil {
			err = in.Lock.Save()
		}
		return adoptedMsg{e, err}
	})
}

func (s *adoptScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if s.step != adoptFinding && s.step != adoptLoading {
			return nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return cmd
	case candidatesMsg:
		// Index errors are not fatal: the user can still enter a source.
		s.err = msg.err
		s.sourceList(msg.cands)
		s.step = adoptSource
		return nil
	case upstreamMsg:
		if msg.err != nil {
			s.err = msg.err
			s.step = adoptOther
			return s.inputs[s.focus].Focus()
		}
		if msg.up != nil {
			s.chooseUpstream(msg.co, msg.up)
			return nil
		}
		s.co = msg.co
		items := make([]pickItem, 0, len(msg.skills))
		for _, sk := range msg.skills {
			items = append(items, pickItem{Title: sk.Name, Desc: sk.Path + " • " + sk.Description, Badge: versionBadge(sk.Version), Value: sk})
		}
		s.list = newPickList(items, false)
		s.list.emptyMsg = "no SKILL.md found in this repository"
		s.list.Focus(func(it pickItem) bool {
			sk := it.Value.(skill.Skill)
			return install.DirName(sk.Name) == s.found.Name
		})
		s.step = adoptPickSkill
		return nil
	case adoptedMsg:
		s.result, s.err = msg.entry, msg.err
		s.step = adoptDone
		return nil
	case tea.KeyMsg:
		return s.key(msg)
	}
	return nil
}

func (s *adoptScreen) key(k tea.KeyMsg) tea.Cmd {
	switch s.step {
	case adoptFinding, adoptLoading:
		if k.String() == "esc" {
			return pop
		}
	case adoptSource:
		if handled, cmd := s.list.Update(k); handled {
			return cmd
		}
		switch k.String() {
		case "esc":
			return pop
		case "enter":
			it, ok := s.list.Current()
			if !ok {
				return nil
			}
			switch v := it.Value.(type) {
			case app.Candidate:
				return s.loadUpstream(v.Entry.Repo.URL, v.Entry.Repo.Ref, v.Entry.Path)
			case otherRepo:
				s.step, s.err = adoptOther, nil
				return s.inputs[s.focus].Focus()
			case localOnly:
				return s.adopt(install.AdoptRequest{Found: s.found})
			}
		}
	case adoptOther:
		switch k.String() {
		case "esc":
			s.inputs[s.focus].Blur()
			s.step, s.err = adoptSource, nil
			return nil
		case "tab", "shift+tab", "up", "down":
			s.inputs[s.focus].Blur()
			s.focus = (s.focus + 1) % len(s.inputs)
			return s.inputs[s.focus].Focus()
		case "enter":
			src, err := source.Normalize(s.inputs[0].Value())
			if err != nil {
				s.err = err
				return nil
			}
			s.inputs[s.focus].Blur()
			return s.loadUpstream(src, strings.TrimSpace(s.inputs[1].Value()), "")
		}
		var cmd tea.Cmd
		s.inputs[s.focus], cmd = s.inputs[s.focus].Update(k)
		return cmd
	case adoptPickSkill:
		if handled, cmd := s.list.Update(k); handled {
			return cmd
		}
		switch k.String() {
		case "esc":
			s.step = adoptOther
			return s.inputs[s.focus].Focus()
		case "enter":
			if it, ok := s.list.Current(); ok {
				sk := it.Value.(skill.Skill)
				s.chooseUpstream(s.co, &sk)
			}
		}
	case adoptTracking:
		if handled, cmd := s.list.Update(k); handled {
			return cmd
		}
		switch k.String() {
		case "esc":
			return s.Init()
		case "enter":
			it, ok := s.list.Current()
			if !ok {
				return nil
			}
			return s.adopt(install.AdoptRequest{Found: s.found, Checkout: s.co, Upstream: s.up, Tracking: it.Value.(lock.Tracking)})
		}
	case adoptDone:
		switch k.String() {
		case "enter", "esc":
			return pop
		}
	}
	return nil
}

func (s *adoptScreen) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Manage %s with faehigkeiten (files stay as they are)\n", accentStyle.Render(s.found.Name))
	b.WriteString(subtleStyle.Render(strings.Join(s.found.Dirs, "\n")) + "\n\n")
	h := s.env.bodyHeight() - 4 - len(s.found.Dirs)
	switch s.step {
	case adoptFinding:
		b.WriteString(s.spin.View() + " looking for the skill in known repositories (the first search indexes them and can take a minute) …")
	case adoptLoading:
		b.WriteString(s.spin.View() + " working …")
	case adoptSource:
		b.WriteString("Where does this skill come from?\n\n")
		b.WriteString(s.list.View(s.env.width, h-2))
	case adoptOther:
		b.WriteString(s.inputs[0].View() + "\n" + s.inputs[1].View())
	case adoptPickSkill:
		b.WriteString("Which skill in " + source.DisplayName(s.co.URL) + " is it?\n\n")
		b.WriteString(s.list.View(s.env.width, h-2))
	case adoptTracking:
		note := warnStyle.Render("Your copy differs from upstream; check for updates to get the upstream version.")
		if s.ident {
			note = okStyle.Render("Your copy is identical to upstream.")
		}
		fmt.Fprintf(&b, "Source: %s (%s)\n%s\n\nHow should updates be tracked?\n\n", source.DisplayName(s.co.URL), s.up.Path, note)
		b.WriteString(s.list.View(s.env.width, h-5))
	case adoptDone:
		if s.err == nil {
			if s.result.Source == "" {
				fmt.Fprintf(&b, "%s %s is now managed (local only)", okStyle.Render("✓"), s.result.Name)
			} else {
				fmt.Fprintf(&b, "%s %s is now managed from %s (%s)", okStyle.Render("✓"), s.result.Name, source.DisplayName(s.result.Source), trackingLabel(s.result.Tracking))
			}
		}
	}
	if s.err != nil {
		b.WriteString("\n\n" + errStyle.Render(s.err.Error()))
	}
	return b.String()
}
