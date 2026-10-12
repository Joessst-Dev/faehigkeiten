package tui

import (
	"context"
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
		ident  bool // up is identical to the local copy
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
	res    *app.Resolver // shared by the lookups between Init and adopting
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
	// A new Resolver each time, so going back looks at the files again.
	s.res = s.env.app.NewResolver()
	e, f, res := s.env, s.found, s.res
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		cands, err := res.Candidates(e.ctx, f)
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
	e, name, res, local := s.env, s.found.Name, s.res, s.found.Dirs[0]
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		if path == "" {
			// Unknown location: list the repository so the user can pick.
			co, skills, err := res.Load(e.ctx, src, ref)
			return upstreamMsg{co: co, skills: skills, err: err}
		}
		// Reuses the repository and hashes from Init when they are known.
		co, up, err := res.Upstream(e.ctx, src, ref, path, name)
		if err != nil {
			return upstreamMsg{err: err}
		}
		return upstreamMsg{co: co, up: up, ident: res.Identical(up.Dir, local)}
	}))
}

// pickUpstream compares a skill picked from a repository listing with the
// local copy in the background, then continues like loadUpstream.
func (s *adoptScreen) pickUpstream(co *source.Checkout, up *skill.Skill) tea.Cmd {
	s.step, s.err = adoptLoading, nil
	res, local := s.res, s.found.Dirs[0]
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		return upstreamMsg{co: co, up: up, ident: res.Identical(up.Dir, local)}
	}))
}

func (s *adoptScreen) chooseUpstream(co *source.Checkout, up *skill.Skill, ident bool) {
	s.co, s.up, s.ident = co, up, ident
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
			if err = in.Lock.Save(); err != nil {
				// Undo, so that a later save does not record it anyway.
				in.Lock.Remove(e.Name)
			}
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
			// The Resolver remembers the failure; let a retry fetch again.
			s.res = s.env.app.NewResolver()
			s.step = adoptOther
			return s.inputs[s.focus].Focus()
		}
		if msg.up != nil {
			s.chooseUpstream(msg.co, msg.up, msg.ident)
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
				return s.pickUpstream(s.co, &sk)
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
	b.WriteString(subtleStyle.Render(strings.Join(s.found.Dirs, "\n")))
	b.WriteString("\n\n")
	h := s.env.bodyHeight() - 4 - len(s.found.Dirs)
	switch s.step {
	case adoptFinding:
		b.WriteString(s.spin.View())
		b.WriteString(" looking for the skill in known repositories (the first search indexes them and can take a minute) …")
	case adoptLoading:
		b.WriteString(s.spin.View())
		b.WriteString(" working …")
	case adoptSource:
		b.WriteString("Where does this skill come from?\n\n")
		b.WriteString(s.list.View(s.env.bodyWidth(), h-2))
	case adoptOther:
		b.WriteString(s.inputs[0].View())
		b.WriteString("\n")
		b.WriteString(s.inputs[1].View())
	case adoptPickSkill:
		fmt.Fprintf(&b, "Which skill in %s is it?\n\n", source.DisplayName(s.co.URL))
		b.WriteString(s.list.View(s.env.bodyWidth(), h-2))
	case adoptTracking:
		note := warnStyle.Render("Your copy differs from upstream; check for updates to get the upstream version.")
		if s.ident {
			note = okStyle.Render("Your copy is identical to upstream.")
		}
		fmt.Fprintf(&b, "Source: %s (%s)\n%s\n\nHow should updates be tracked?\n\n", source.DisplayName(s.co.URL), s.up.Path, note)
		b.WriteString(s.list.View(s.env.bodyWidth(), h-5))
	case adoptDone:
		if s.err == nil {
			b.WriteString(adoptedLine(s.result))
		}
	}
	if s.err != nil {
		b.WriteString("\n\n")
		b.WriteString(errStyle.Render(s.err.Error()))
	}
	return b.String()
}

// adoptedLine reports that e is now managed.
func adoptedLine(e lock.Entry) string {
	if e.Source == "" {
		return fmt.Sprintf("%s %s is now managed (local only)", okStyle.Render("✓"), e.Name)
	}
	return fmt.Sprintf("%s %s is now managed from %s (%s)", okStyle.Render("✓"), e.Name, source.DisplayName(e.Source), trackingLabel(e.Tracking))
}

// ---- adopting several skills ----

// adoptManyScreen adopts several unmanaged skills at once. Only sources whose
// content is identical to the local copy are chosen automatically: the user
// has not looked at each skill, and a wrong source would let the next update
// overwrite it. The remaining skills are kept local only or walked through
// adoptScreen one by one.

type (
	// adoptPlan is how one skill will be adopted. Unresolved plans have no
	// source yet; reason says why.
	adoptPlan struct {
		req      install.AdoptRequest
		resolved bool
		reason   string
	}
	plansMsg       struct{ plans []adoptPlan }
	adoptedManyMsg struct {
		entries []lock.Entry
		errs    []error
	}
)

type adoptManyScreen struct {
	env     *env
	in      *install.Installer
	found   []install.Found
	step    adoptStep
	spin    spinner.Model
	list    *pickList
	plans   []adoptPlan
	entries []lock.Entry
	errs    []error
	queue   []install.Found // skills still needing a source
	current *install.Found  // skill open in adoptScreen during the walk
	cancel  context.CancelFunc
}

func newAdoptManyScreen(e *env, in *install.Installer, found []install.Found) *adoptManyScreen {
	return &adoptManyScreen{env: e, in: in, found: found, spin: newSpinner()}
}

func (s *adoptManyScreen) Title() string { return fmt.Sprintf("Manage %d skills", len(s.found)) }

func (s *adoptManyScreen) Help() string {
	switch s.step {
	case adoptSource:
		return "↑/↓ move • enter adopt, then choose missing sources • l adopt, keep the rest local only • esc cancel"
	case adoptDone:
		if len(s.queue) > 0 {
			return "enter choose missing sources • esc done"
		}
		return "enter done"
	}
	return ""
}

func (s *adoptManyScreen) Init() tea.Cmd {
	s.step = adoptFinding
	ctx, cancel := context.WithCancel(s.env.ctx)
	s.cancel = cancel
	a, found := s.env.app, s.found
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		defer cancel()
		res := a.NewResolver().ResolveAll(ctx, found, app.AdoptOptions{IdenticalOnly: true})
		if ctx.Err() != nil {
			// Left the screen: the results are incomplete.
			return plansMsg{}
		}
		plans := make([]adoptPlan, len(found))
		for i, r := range res {
			plans[i] = adoptPlan{req: install.AdoptRequest{Found: found[i]}}
			if r.Err != nil {
				plans[i].reason = r.Err.Error()
			} else {
				plans[i].req, plans[i].resolved = r.Req, true
			}
		}
		return plansMsg{plans}
	}))
}

// Resume runs when adoptScreen returns during the walk. A skill that was not
// adopted means the user backed out or it failed: the walk stops, and the
// skill moves to the end of the queue so it does not block the others.
func (s *adoptManyScreen) Resume() tea.Cmd {
	if s.current == nil {
		return nil
	}
	f := *s.current
	s.current = nil
	e, ok := s.in.Lock.Get(f.Name)
	if !ok {
		s.queue = append(s.queue, f)
		return nil
	}
	s.entries = append(s.entries, e)
	if len(s.queue) == 0 {
		return nil
	}
	return s.next()
}

func (s *adoptManyScreen) next() tea.Cmd {
	f := s.queue[0]
	s.queue = s.queue[1:]
	s.current = &f
	return push(newAdoptScreen(s.env, s.in, f))
}

func (s *adoptManyScreen) reviewList() {
	items := make([]pickItem, 0, len(s.plans))
	for _, p := range s.plans {
		it := pickItem{Title: p.req.Found.Name, Badge: "needs a source", Desc: p.reason}
		if p.resolved {
			it.Badge = "identical"
			it.Desc = "from " + source.DisplayName(p.req.Checkout.URL) + " • " + trackingLabel(p.req.Tracking)
		}
		items = append(items, it)
	}
	s.list = newPickList(items, false)
}

// adopt adopts all resolved skills; unresolved ones are kept local only when
// local is set and queued for adoptScreen otherwise.
func (s *adoptManyScreen) adopt(local bool) tea.Cmd {
	s.step = adoptLoading
	s.queue = nil
	var reqs []install.AdoptRequest
	for _, p := range s.plans {
		switch {
		case p.resolved:
			reqs = append(reqs, p.req)
		case local:
			reqs = append(reqs, install.AdoptRequest{Found: p.req.Found})
		default:
			s.queue = append(s.queue, p.req.Found)
		}
	}
	in := s.in
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		entries, errs, err := in.AdoptAll(reqs)
		if err != nil {
			errs = append(errs, err)
		}
		return adoptedManyMsg{entries, errs}
	}))
}

func (s *adoptManyScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if s.step != adoptFinding && s.step != adoptLoading {
			return nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return cmd
	case plansMsg:
		s.plans = msg.plans
		s.reviewList()
		s.step = adoptSource
		return nil
	case adoptedManyMsg:
		s.entries, s.errs = msg.entries, msg.errs
		s.step = adoptDone
		return nil
	case tea.KeyMsg:
		return s.key(msg)
	}
	return nil
}

func (s *adoptManyScreen) key(k tea.KeyMsg) tea.Cmd {
	switch s.step {
	case adoptFinding:
		if k.String() == "esc" {
			s.cancel()
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
			return s.adopt(false)
		case "l":
			return s.adopt(true)
		}
	case adoptDone:
		switch k.String() {
		case "enter":
			if len(s.queue) > 0 {
				return s.next()
			}
			return pop
		case "esc":
			return pop
		}
	}
	return nil
}

func (s *adoptManyScreen) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Manage %d skills with faehigkeiten (files stay as they are)\n\n", len(s.found))
	switch s.step {
	case adoptFinding:
		b.WriteString(s.spin.View())
		b.WriteString(" looking for the skills in known repositories (the first search indexes them and can take a minute) …")
	case adoptLoading:
		b.WriteString(s.spin.View())
		b.WriteString(" working …")
	case adoptSource:
		b.WriteString("Only sources identical to your copies are chosen automatically.\n\n")
		b.WriteString(s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4))
	case adoptDone:
		for _, e := range s.entries {
			b.WriteString(adoptedLine(e))
			b.WriteString("\n")
		}
		for _, err := range s.errs {
			b.WriteString(errStyle.Render(err.Error()))
			b.WriteString("\n")
		}
		if n := len(s.queue); n > 0 {
			fmt.Fprintf(&b, "\n%s", warnStyle.Render(fmt.Sprintf("%d skill(s) still need a source; press enter to choose them one by one.", n)))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
