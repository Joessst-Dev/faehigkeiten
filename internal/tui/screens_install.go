package tui

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/lock"
	"github.com/Joessst-Dev/faehigkeiten/internal/skill"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
	"github.com/Joessst-Dev/faehigkeiten/internal/update"
)

// ---- install wizard ----

type wizardStep int

const (
	stepTarget wizardStep = iota
	stepAgents
	stepTracking
	stepConfirm
	stepRunning
	stepDone
)

// trackingPolicy decides the tracking mode per skill when installing several.
type trackingPolicy struct {
	label, desc string
	pick        func(skill.Skill) lock.Tracking
}

var policies = []trackingPolicy{
	{"Version, otherwise hash", "Track versioned skills by version and all others by content hash", func(s skill.Skill) lock.Tracking {
		if s.HasVersion() {
			return lock.TrackVersion
		}
		return lock.TrackHash
	}},
	{"Version, otherwise untracked", "Track versioned skills by version; skip update checks for the rest", install.DefaultTracking},
	{"Hash for all", "Track every skill by content hash", func(skill.Skill) lock.Tracking { return lock.TrackHash }},
	{"Untracked", "Never check these skills for updates", func(skill.Skill) lock.Tracking { return lock.TrackNone }},
}

type installResult struct {
	name  string
	entry lock.Entry
	err   error
}

type installDoneMsg struct {
	results []installResult
	saveErr error
}

type wizardScreen struct {
	env    *env
	co     *source.Checkout
	skills []skill.Skill
	step   wizardStep
	target install.Target
	agents *pickList
	track  *pickList
	spin   spinner.Model
	res    installDoneMsg
	force  bool
	err    error
}

func newWizard(e *env, co *source.Checkout, skills []skill.Skill) *wizardScreen {
	w := &wizardScreen{env: e, co: co, skills: skills, target: e.target, spin: newSpinner()}
	w.track = newPickList(w.trackingItems(), false)
	return w
}

func (w *wizardScreen) trackingItems() []pickItem {
	if len(w.skills) == 1 {
		s := w.skills[0]
		var items []pickItem
		for _, t := range install.AvailableTracking(s) {
			items = append(items, pickItem{Title: trackingLabel(t), Desc: trackingDesc(t, s), Value: t})
		}
		return items
	}
	var items []pickItem
	for i, p := range policies {
		items = append(items, pickItem{Title: p.label, Desc: p.desc, Value: i})
	}
	return items
}

func trackingLabel(t lock.Tracking) string {
	switch t {
	case lock.TrackVersion:
		return "Track version"
	case lock.TrackHash:
		return "Track content hash"
	}
	return "Untracked"
}

func trackingDesc(t lock.Tracking, s skill.Skill) string {
	switch t {
	case lock.TrackVersion:
		return "Updates are offered when the version in SKILL.md increases (now " + s.Version + ")"
	case lock.TrackHash:
		return "Stores a SHA-256 of the skill in the lockfile; updates are offered when upstream content changes"
	}
	return "No update checks for this skill"
}

func (w *wizardScreen) Title() string { return "Install" }

func (w *wizardScreen) Help() string {
	switch w.step {
	case stepAgents:
		return "space toggle • a all • enter continue"
	case stepTracking:
		return "↑/↓ move • enter continue"
	case stepConfirm:
		return "enter install"
	case stepDone:
		return "enter done"
	}
	return ""
}

func (w *wizardScreen) Init() tea.Cmd {
	return push(newTargetPicker(w.env, w.target.Scope, w.target.ProjectRoot, func(c targetChoice) {
		t, err := w.env.app.Target(c.scope, c.root)
		if err != nil {
			w.err = err
			return
		}
		w.target = t
		w.step = stepAgents
		w.agents = w.agentItems()
	}))
}

// Resume is called when the target picker closes; cancelling it cancels the wizard.
func (w *wizardScreen) Resume() tea.Cmd {
	if w.step == stepTarget {
		return pop
	}
	return nil
}

func (w *wizardScreen) agentItems() *pickList {
	pre := w.env.app.DefaultAgents(w.target)
	var items []pickItem
	for _, a := range w.env.app.Agents.All() {
		dir, err := a.Dir(w.target.Scope, w.target.ProjectRoot, w.target.Home)
		if err != nil {
			continue
		}
		items = append(items, pickItem{Title: a.Name, Desc: dir, Checked: slices.Contains(pre, a.ID), Value: a})
	}
	return newPickList(items, true)
}

func (w *wizardScreen) chosenAgents() []agent.Agent {
	var out []agent.Agent
	for _, it := range w.agents.Checked() {
		out = append(out, it.Value.(agent.Agent))
	}
	return out
}

func (w *wizardScreen) trackingFor(s skill.Skill) lock.Tracking {
	it, _ := w.track.Current()
	if t, ok := it.Value.(lock.Tracking); ok {
		return t
	}
	return policies[it.Value.(int)].pick(s)
}

func (w *wizardScreen) run() tea.Cmd {
	w.step = stepRunning
	e, co, skills, target, agents, force := w.env, w.co, w.skills, w.target, w.chosenAgents(), w.force
	tracking := make([]lock.Tracking, len(skills))
	for i, s := range skills {
		tracking[i] = w.trackingFor(s)
	}
	return tea.Batch(w.spin.Tick, async(w, func() tea.Msg {
		in, err := e.app.Installer(target)
		if err != nil {
			return installDoneMsg{saveErr: err}
		}
		var res []installResult
		for i, s := range skills {
			entry, err := in.Install(install.Request{Skill: s, Checkout: co, Agents: agents, Tracking: tracking[i], Force: force})
			res = append(res, installResult{name: s.Name, entry: entry, err: err})
		}
		return installDoneMsg{results: res, saveErr: in.Lock.Save()}
	}))
}

func (w *wizardScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if w.step != stepRunning {
			return nil
		}
		var cmd tea.Cmd
		w.spin, cmd = w.spin.Update(msg)
		return cmd
	case installDoneMsg:
		w.step, w.res = stepDone, msg
		return nil
	case tea.KeyMsg:
		return w.key(msg)
	}
	return nil
}

func (w *wizardScreen) key(k tea.KeyMsg) tea.Cmd {
	switch w.step {
	case stepAgents:
		if handled, cmd := w.agents.Update(k); handled {
			return cmd
		}
		switch k.String() {
		case "esc":
			w.step = stepTarget
			return w.Init()
		case "enter":
			if len(w.agents.Checked()) == 0 {
				w.err = errors.New("select at least one agent")
				return nil
			}
			w.err = nil
			w.step = stepTracking
		}
	case stepTracking:
		if handled, cmd := w.track.Update(k); handled {
			return cmd
		}
		switch k.String() {
		case "esc":
			w.step = stepAgents
		case "enter":
			w.step = stepConfirm
		}
	case stepConfirm:
		switch k.String() {
		case "esc":
			w.step = stepTracking
		case "enter", "y":
			return w.run()
		}
	case stepDone:
		switch k.String() {
		case "f":
			if w.hasConflicts() {
				w.force = true
				return w.run()
			}
		case "enter", "esc", "q":
			return pop
		}
	}
	return nil
}

func (w *wizardScreen) hasConflicts() bool {
	for _, r := range w.res.results {
		if errors.Is(r.err, install.ErrConflict) {
			return true
		}
	}
	return false
}

func (w *wizardScreen) View() string {
	var b strings.Builder
	names := make([]string, len(w.skills))
	for i, s := range w.skills {
		names[i] = s.Name
	}
	fmt.Fprintf(&b, "Installing %s from %s\n", accentStyle.Render(strings.Join(names, ", ")), source.DisplayName(w.co.URL))
	fmt.Fprintf(&b, "%s\n\n", subtleStyle.Render("into "+describeTarget(w.target)))
	h := w.env.bodyHeight() - 4
	switch w.step {
	case stepTarget:
		b.WriteString(subtleStyle.Render("choose a target …"))
	case stepAgents:
		b.WriteString("Which agents should get the skill?\n\n")
		b.WriteString(w.agents.View(w.env.bodyWidth(), h-2))
	case stepTracking:
		b.WriteString("How should updates be tracked?\n\n")
		b.WriteString(w.track.View(w.env.bodyWidth(), h-2))
	case stepConfirm:
		var agents []string
		for _, a := range w.chosenAgents() {
			agents = append(agents, a.Name)
		}
		fmt.Fprintf(&b, "Agents:   %s\n", strings.Join(agents, ", "))
		for _, s := range w.skills {
			fmt.Fprintf(&b, "%-24s %s\n", s.Name, subtleStyle.Render(trackingLabel(w.trackingFor(s))))
		}
		b.WriteString("\nPress enter to install.")
	case stepRunning:
		b.WriteString(w.spin.View())
		b.WriteString(" installing …")
	case stepDone:
		for _, r := range w.res.results {
			if r.err != nil {
				fmt.Fprintf(&b, "%s %s: %s\n", errStyle.Render("✗"), r.name, r.err)
				continue
			}
			fmt.Fprintf(&b, "%s %s %s\n", okStyle.Render("✓"), r.name, subtleStyle.Render(trackingLabel(r.entry.Tracking)))
		}
		if w.res.saveErr != nil {
			fmt.Fprintf(&b, "\n%s\n", errStyle.Render("lockfile: "+w.res.saveErr.Error()))
		}
		if w.hasConflicts() {
			b.WriteString("\n")
			b.WriteString(warnStyle.Render("Some directories already exist. Press f to overwrite them."))
		}
	}
	if w.err != nil {
		b.WriteString("\n\n")
		b.WriteString(errStyle.Render(w.err.Error()))
	}
	return b.String()
}

// ---- installed skills ----

type installedScreen struct {
	env     *env
	list    *pickList
	in      *install.Installer
	confirm any // lock.Entry or install.Found awaiting confirmation
	msg     string
	err     error
}

func newInstalledScreen(e *env) *installedScreen {
	l := newPickList(nil, false)
	l.emptyMsg = "no skills installed here yet"
	return &installedScreen{env: e, list: l}
}

func (s *installedScreen) Title() string   { return "Installed" }
func (s *installedScreen) Init() tea.Cmd   { s.reload(); return nil }
func (s *installedScreen) Resume() tea.Cmd { s.reload(); return nil }

func (s *installedScreen) Help() string {
	if s.confirm != nil {
		return "y confirm • n cancel"
	}
	if it, ok := s.list.Current(); ok {
		if _, unmanaged := it.Value.(install.Found); unmanaged {
			return "m manage • d remove • u check updates • t change target • / filter"
		}
	}
	return "d remove • u check updates • t change target • / filter"
}

func (s *installedScreen) reload() {
	in, err := s.env.app.Installer(s.env.target)
	s.in, s.err = in, err
	if err != nil {
		s.list.SetItems(nil)
		return
	}
	found := in.Unmanaged()
	items := make([]pickItem, 0, len(in.Lock.Skills)+len(found))
	for _, e := range in.Lock.Skills {
		items = append(items, pickItem{
			Title: e.Name,
			Badge: entryBadge(e),
			Desc:  source.DisplayName(e.Source) + " • " + strings.Join(e.Agents, ", "),
			Value: e,
		})
	}
	for _, f := range found {
		desc := strings.Join(f.Agents, ", ")
		if f.Description != "" {
			desc += " • " + f.Description
		}
		items = append(items, pickItem{Title: f.Name, Badge: "not managed", Desc: desc, Value: f})
	}
	s.list.SetItems(items)
}

func entryBadge(e lock.Entry) string {
	switch e.Tracking {
	case lock.TrackVersion:
		return "v" + strings.TrimPrefix(e.Version, "v")
	case lock.TrackHash:
		h := strings.TrimPrefix(e.Hash, skill.HashPrefix)
		return "hash " + h[:min(8, len(h))]
	}
	return "untracked"
}

func (s *installedScreen) remove(v any) error {
	switch v := v.(type) {
	case lock.Entry:
		if err := s.in.Uninstall(v.Name); err != nil {
			return err
		}
		return s.in.Lock.Save()
	case install.Found:
		return install.RemoveFound(v)
	}
	return nil
}

func confirmName(v any) string {
	switch v := v.(type) {
	case lock.Entry:
		return v.Name
	case install.Found:
		return v.Name
	}
	return ""
}

func (s *installedScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	if s.confirm != nil {
		v := s.confirm
		s.confirm = nil
		if k.String() != "y" {
			return nil
		}
		if err := s.remove(v); err != nil {
			s.msg = err.Error()
			return nil
		}
		s.msg = "removed " + confirmName(v)
		s.reload()
		return nil
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "d":
		if it, ok := s.list.Current(); ok {
			s.confirm = it.Value
		}
	case "m":
		if it, ok := s.list.Current(); ok {
			if f, ok := it.Value.(install.Found); ok {
				return push(newAdoptScreen(s.env, s.in, f))
			}
		}
	case "u":
		return push(newUpdatesScreen(s.env))
	case "t":
		return push(newTargetScreen(s.env))
	}
	return nil
}

func (s *installedScreen) View() string {
	if s.err != nil {
		return errStyle.Render(s.err.Error())
	}
	v := subtleStyle.Render("lockfile: "+s.in.Lock.Path()) + "\n\n" + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4)
	switch c := s.confirm.(type) {
	case lock.Entry:
		v += "\n" + warnStyle.Render("Remove "+c.Name+" from all agents? (y/n)")
	case install.Found:
		v += "\n" + warnStyle.Render("Delete "+c.Name+"? It was not installed by faehigkeiten. (y/n)") +
			"\n" + subtleStyle.Render(strings.Join(c.Dirs, "\n"))
	default:
		if s.msg != "" {
			v += "\n" + okStyle.Render(s.msg)
		}
	}
	return v
}

// ---- updates ----

type (
	checkedMsg struct {
		in       *install.Installer
		statuses []update.Status
		err      error
	}
	appliedMsg struct {
		updated []string
		errs    []error
	}
)

type updatesScreen struct {
	env      *env
	list     *pickList
	spin     spinner.Model
	busy     string
	in       *install.Installer
	statuses []update.Status
	msg      string
	errs     []error
	err      error
}

func newUpdatesScreen(e *env) *updatesScreen {
	l := newPickList(nil, true)
	l.emptyMsg = "no tracked skills in this target"
	return &updatesScreen{env: e, list: l, spin: newSpinner()}
}

func (s *updatesScreen) Title() string { return "Updates" }
func (s *updatesScreen) Help() string {
	if s.busy != "" {
		return ""
	}
	return "space toggle • enter update selected • r re-check"
}

func (s *updatesScreen) Init() tea.Cmd { return s.check() }

func (s *updatesScreen) check() tea.Cmd {
	s.busy = "checking sources"
	e := s.env
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		in, err := e.app.Installer(e.target)
		if err != nil {
			return checkedMsg{err: err}
		}
		return checkedMsg{in: in, statuses: update.Check(e.ctx, e.app.Sources, in)}
	}))
}

func (s *updatesScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		if s.busy == "" {
			return nil
		}
		var cmd tea.Cmd
		s.spin, cmd = s.spin.Update(msg)
		return cmd
	case checkedMsg:
		s.busy, s.err, s.statuses, s.in = "", msg.err, msg.statuses, msg.in
		items := make([]pickItem, 0, len(msg.statuses))
		for i, st := range msg.statuses {
			it := pickItem{Title: st.Entry.Name, Value: i}
			switch {
			case st.Err != nil:
				it.Disabled, it.Badge, it.Desc = true, "error", st.Err.Error()
			case st.Available && st.Modified:
				// Not preselected: updating overwrites the local edits.
				it.Badge = "update available • locally modified"
				it.Desc = fmt.Sprintf("%s: %s → %s • updating overwrites your local changes", st.Entry.Tracking, short(st.Current), short(st.Latest))
			case st.Available:
				it.Checked, it.Badge = true, "update available"
				it.Desc = fmt.Sprintf("%s: %s → %s", st.Entry.Tracking, short(st.Current), short(st.Latest))
			case st.Modified:
				it.Disabled, it.Badge = true, "locally modified"
				it.Desc = fmt.Sprintf("%s: %s (up to date)", st.Entry.Tracking, short(st.Current))
			default:
				it.Disabled, it.Desc = true, fmt.Sprintf("%s: %s (up to date)", st.Entry.Tracking, short(st.Current))
			}
			items = append(items, it)
		}
		s.list.SetItems(items)
		return nil
	case appliedMsg:
		s.busy, s.errs = "", msg.errs
		s.msg = fmt.Sprintf("updated %d skill(s)", len(msg.updated))
		return s.check()
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
		case "r":
			s.msg, s.errs = "", nil
			return s.check()
		case "enter":
			return s.apply()
		}
	}
	return nil
}

func short(v string) string {
	v = strings.TrimPrefix(v, skill.HashPrefix)
	if len(v) > 12 {
		return v[:12]
	}
	if v == "" {
		return "-"
	}
	return v
}

func (s *updatesScreen) apply() tea.Cmd {
	var picked []update.Status
	for _, it := range s.list.Checked() {
		picked = append(picked, s.statuses[it.Value.(int)])
	}
	if len(picked) == 0 || s.in == nil {
		return nil
	}
	s.busy = "updating"
	in := s.in
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		var res appliedMsg
		for _, st := range picked {
			if _, err := update.Apply(in, st); err != nil {
				res.errs = append(res.errs, fmt.Errorf("%s: %w", st.Entry.Name, err))
				continue
			}
			res.updated = append(res.updated, st.Entry.Name)
		}
		if err := in.Lock.Save(); err != nil {
			res.errs = append(res.errs, err)
		}
		return res
	}))
}

func (s *updatesScreen) modifiedUpdates() int {
	n := 0
	for _, st := range s.statuses {
		if st.Err == nil && st.Available && st.Modified {
			n++
		}
	}
	return n
}

func (s *updatesScreen) View() string {
	if s.busy != "" {
		return s.spin.View() + " " + s.busy + " …"
	}
	if s.err != nil {
		return errStyle.Render(s.err.Error())
	}
	var b strings.Builder
	b.WriteString(subtleStyle.Render("Untracked skills are not checked."))
	b.WriteString("\n")
	if n := s.modifiedUpdates(); n > 0 {
		b.WriteString(warnStyle.Render(fmt.Sprintf(
			"Warning: %d skill(s) with updates were modified locally. Updating them overwrites your changes, so they are not selected.", n)))
	}
	b.WriteString("\n")
	b.WriteString(s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4))
	if s.msg != "" {
		b.WriteString("\n")
		b.WriteString(okStyle.Render(s.msg))
	}
	for _, err := range s.errs {
		b.WriteString("\n")
		b.WriteString(errStyle.Render(err.Error()))
	}
	return b.String()
}
