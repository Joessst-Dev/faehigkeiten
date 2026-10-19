package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
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
	msg      string
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
	return "space select • a all • enter install • v versions • l license • f favourite • F favourites only • / filter • r refresh"
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
		return tick(&s.spin, s.loading, msg)
	case skillsLoadedMsg:
		s.loading = false
		if msg.err != nil {
			s.err = msg.err
			return nil
		}
		s.co = msg.co
		items := make([]pickItem, 0, len(msg.skills))
		for _, sk := range msg.skills {
			items = append(items, pickItem{
				Title:   sk.Name,
				Desc:    withLicense(sk.Description, sk.License.Label()),
				Badge:   versionBadge(sk.Version),
				Starred: s.env.app.Config.IsFavourite(s.co.URL, sk.Path),
				Value:   sk,
			})
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
		case "f":
			it, ok := s.list.Current()
			if !ok || s.co == nil {
				return nil
			}
			sk := it.Value.(skill.Skill)
			msg, err := toggleStar(s.env, s.list, it, config.Favourite{Name: sk.Name, URL: s.co.URL, Ref: s.ref, Path: sk.Path})
			if err != nil {
				msg = err.Error()
			}
			s.msg = msg
		case "F":
			s.list.SetFavouritesOnly(!s.list.FavouritesOnly())
		case "v":
			it, ok := s.list.Current()
			if !ok || s.co == nil {
				return nil
			}
			return push(newVersionsScreen(s.env, s.co, it.Value.(skill.Skill)))
		case "l":
			it, ok := s.list.Current()
			if !ok || s.co == nil {
				return nil
			}
			return push(licenseOfSkill(s.env, s.co, it.Value.(skill.Skill)))
		case "enter":
			if s.co == nil {
				return nil
			}
			if picked := s.pickedSkills(); len(picked) > 0 {
				return push(newWizard(s.env, s.co, picked))
			}
		}
	}
	return nil
}

// pickedSkills returns the checked skills, or the one under the cursor when
// none are checked.
func (s *skillsScreen) pickedSkills() []skill.Skill {
	var picked []skill.Skill
	for _, it := range s.list.Checked() {
		picked = append(picked, it.Value.(skill.Skill))
	}
	if len(picked) == 0 {
		if it, ok := s.list.Current(); ok {
			picked = []skill.Skill{it.Value.(skill.Skill)}
		}
	}
	return picked
}

// withLicense appends a license label to a list item description.
func withLicense(desc, label string) string {
	switch {
	case label == "":
		return desc
	case desc == "":
		return label
	}
	return desc + " • " + label
}

func versionBadge(v string) string {
	if v == "" {
		return "no version"
	}
	return "v" + strings.TrimPrefix(v, "v")
}

func (s *skillsScreen) View() string {
	if s.loading {
		return spinLine(s.spin, "fetching "+s.src)
	}
	if s.err != nil {
		return errStyle.Render("Could not load repository: "+s.err.Error()) + "\n\n" + subtleStyle.Render("press r to retry")
	}
	head := subtleStyle.Render(fmt.Sprintf("%s @ %s • %d skills", s.co.URL, checkoutLabel(s.co), len(s.list.items)))
	if s.msg == "" {
		return head + "\n\n" + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-2)
	}
	return head + "\n\n" + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-3) + "\n" + warnStyle.Render(s.msg)
}

func checkoutLabel(co *source.Checkout) string {
	if co.Commit == "" {
		return "local"
	}
	ref := co.Ref
	if ref == "" {
		ref = "HEAD"
	}
	return ref + " " + source.ShortCommit(co.Commit)
}

// ---- versions of one skill ----

type (
	versionsLoadedMsg struct {
		versions []source.Version
		err      error
	}
	latestLoadedMsg struct {
		co    *source.Checkout
		skill skill.Skill
		err   error
	}
)

type versionsScreen struct {
	env   *env
	co    *source.Checkout
	skill skill.Skill
	busy  string
	spin  spinner.Model
	list  *pickList
	err   error
}

// newVersionsScreen lists the versions in the history of a skill's SKILL.md
// in the checkout co and installs the chosen one: the current version
// unpinned, older ones pinned.
func newVersionsScreen(e *env, co *source.Checkout, sk skill.Skill) *versionsScreen {
	l := newPickList(nil, false)
	return &versionsScreen{env: e, co: co, skill: sk, spin: newSpinner(), list: l}
}

func (s *versionsScreen) Title() string { return s.skill.Name + " versions" }
func (s *versionsScreen) Help() string {
	if s.busy != "" {
		return ""
	}
	return "enter install • / filter"
}

func (s *versionsScreen) Init() tea.Cmd {
	s.busy = "reading history"
	e := s.env
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		vs, err := e.app.Versions(e.ctx, s.co.URL, s.co.Ref, s.skill.Path, true)
		return versionsLoadedMsg{vs, err}
	}))
}

func (s *versionsScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return tick(&s.spin, s.busy != "", msg)
	case versionsLoadedMsg:
		s.busy, s.err = "", msg.err
		items := make([]pickItem, 0, len(msg.versions))
		for _, v := range msg.versions {
			it := pickItem{
				Title: versionBadge(v.Version),
				Desc:  source.ShortCommit(v.Commit) + " • " + v.Date.Format(time.DateOnly),
				Value: v,
			}
			if v.Latest {
				it.Badge = "latest (unpinned)"
			} else {
				it.Badge = "pinned"
			}
			items = append(items, it)
		}
		s.list.SetItems(items)
		return nil
	case latestLoadedMsg:
		s.busy = ""
		if msg.err != nil {
			s.err = msg.err
			return nil
		}
		return replace(s.latestWizard(msg.co, msg.skill))
	case tea.KeyMsg:
		if s.busy != "" {
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
		case "enter":
			it, ok := s.list.Current()
			if !ok {
				return nil
			}
			v := it.Value.(source.Version)
			if !v.Latest {
				w := newWizard(s.env, s.co, []skill.Skill{s.skill})
				// The commit is known, so the install needs no second walk.
				w.sel = app.Selector{Version: v.Version, Commit: v.Commit}
				return replace(w)
			}
			// The skills screen may show an older checkout than the history,
			// even with the same version, so install the newest commit.
			s.busy, s.err = "fetching "+versionBadge(v.Version), nil
			e := s.env
			return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
				co, sk, err := e.app.LoadCommit(e.ctx, s.co.URL, s.co.Ref, s.skill.Path, v.Commit)
				return latestLoadedMsg{co, sk, err}
			}))
		}
	}
	return nil
}

// latestWizard installs the latest version like the skills screen does, but
// replacing a pin instead of keeping it.
func (s *versionsScreen) latestWizard(co *source.Checkout, sk skill.Skill) *wizardScreen {
	w := newWizard(s.env, co, []skill.Skill{sk})
	w.sel = app.Selector{Version: app.Latest}
	return w
}

func (s *versionsScreen) View() string {
	if s.busy != "" {
		return spinLine(s.spin, s.busy)
	}
	var b strings.Builder
	b.WriteString(subtleStyle.Render("Older versions are installed pinned: they are not updated until you unpin them. The latest version removes a pin."))
	b.WriteString("\n\n")
	if s.err != nil {
		b.WriteString(errStyle.Render(s.err.Error()))
		return b.String()
	}
	b.WriteString(s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-2))
	return b.String()
}

// ---- search ----

type (
	// indexTickMsg polls the background indexer while it builds.
	indexTickMsg     struct{}
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
	ist     registry.IndexStatus
	idxErr  error // from the last index build; err is for remote search
	polling bool
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
	mode, favs := "tab search skills.sh", "ctrl+f favourite • ctrl+o favourites only • "
	if s.remote {
		mode, favs = "tab search known repositories", "" // skills.sh hits cannot be favourites
	}
	return "↑/↓ move • enter open • ctrl+l license • " + favs + mode + " • ctrl+r refresh index"
}

// Resume shows favourites marked or unmarked on a screen opened from here.
func (s *searchScreen) Resume() tea.Cmd {
	if !s.remote {
		s.refreshLocal()
	}
	return nil
}

func (s *searchScreen) Init() tea.Cmd {
	return tea.Batch(s.input.Focus(), s.loadIndex(false))
}

// loadIndex starts the background indexer and shows its results as they
// arrive.
func (s *searchScreen) loadIndex(refresh bool) tea.Cmd {
	s.env.app.Indexer().Start(s.env.ctx, refresh, nil)
	return s.syncIndex(true)
}

// syncIndex picks up the indexer's current state (always when force is set,
// otherwise only when it changed) and keeps one polling loop running while
// it builds.
func (s *searchScreen) syncIndex(force bool) tea.Cmd {
	idx, st := s.env.app.Indexer().Snapshot()
	if !force && !st.Building && st.Gen != s.ist.Gen && errors.Is(st.Err, context.Canceled) && s.env.ctx.Err() == nil {
		// Another caller cancelled the shared build (e.g. it left its
		// screen); finish it for this screen.
		s.env.app.Indexer().Start(s.env.ctx, false, nil)
		idx, st = s.env.app.Indexer().Snapshot()
	}
	if force || st.Gen != s.ist.Gen {
		s.idx, s.idxErr = idx, nil
		if st.Err != nil && !errors.Is(st.Err, context.Canceled) {
			s.idxErr = st.Err
		}
		if !s.remote {
			s.refreshLocal()
		}
	}
	s.ist = st
	if !st.Building || s.polling {
		return nil
	}
	s.polling = true
	poll := tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return routed{to: s, msg: indexTickMsg{}} })
	if s.loading {
		return poll // the spinner is already ticking
	}
	return tea.Batch(poll, s.spin.Tick)
}

func (s *searchScreen) searchRemote() tea.Cmd {
	q := strings.TrimSpace(s.input.Value())
	if q == "" {
		return nil
	}
	var spin tea.Cmd
	if !s.loading && !s.ist.Building {
		spin = s.spin.Tick // otherwise the spinner is already ticking
	}
	s.lastQ, s.loading, s.err = q, true, nil
	e := s.env
	return tea.Batch(spin, async(s, func() tea.Msg {
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
	for _, h := range hits {
		if len(items) == 200 {
			break
		}
		fav := s.env.app.Config.IsFavourite(h.Repo.URL, h.Path)
		if s.list.FavouritesOnly() && !fav {
			continue // before the cap, so that no favourite is cut off
		}
		items = append(items, pickItem{
			Title:   h.Name,
			Desc:    withLicense(h.Repo.Name+" — "+h.Description, h.License),
			Badge:   versionBadge(h.Version),
			Starred: fav,
			Value:   h,
		})
	}
	s.list.SetItems(items)
}

func (s *searchScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return tick(&s.spin, s.loading || s.ist.Building, msg)
	case indexTickMsg:
		s.polling = false
		return s.syncIndex(false)
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
				s.list.SetFavouritesOnly(false) // skills.sh hits cannot be favourites
				s.lastQ = ""
				return s.searchRemote()
			}
			s.refreshLocal()
			return nil
		case "ctrl+l":
			if it, ok := s.list.Current(); ok {
				if ls := licenseOfHit(s.env, it.Value); ls != nil {
					return push(ls)
				}
			}
			return nil
		case "ctrl+f":
			it, ok := s.list.Current()
			if !ok {
				return nil
			}
			h, ok := it.Value.(registry.Entry)
			if !ok {
				s.err = errors.New("open the skill to mark it as a favourite")
				return nil
			}
			_, s.err = toggleStar(s.env, s.list, it, config.Favourite{Name: h.Name, URL: h.Repo.URL, Ref: h.Repo.Ref, Path: h.Path})
			return nil
		case "ctrl+o":
			if !s.remote {
				s.list.SetFavouritesOnly(!s.list.FavouritesOnly())
				s.refreshLocal()
			}
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
	case s.loading:
		v += spinLine(s.spin, "searching") + "\n"
	case s.ist.Building && s.ist.Total > 0 && !s.remote:
		v += spinLine(s.spin, fmt.Sprintf("indexing repositories %d/%d", s.ist.Done, s.ist.Total)) + "\n"
	}
	if s.err != nil {
		v += errStyle.Render(s.err.Error()) + "\n"
	}
	if s.idxErr != nil && !s.remote {
		v += errStyle.Render(s.idxErr.Error()) + "\n"
	}
	if s.idx != nil && !s.remote && len(s.idx.Errors) > 0 {
		v += warnStyle.Render(fmt.Sprintf("%d repositories could not be indexed", len(s.idx.Errors))) + "\n"
	}
	return v + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-5)
}
