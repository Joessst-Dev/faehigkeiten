package tui

import (
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/app"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
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
	// sel is passed to app.Prepare for every skill.
	sel app.Selector
	// pins maps the names of skills that will be installed pinned to their
	// version. Pinned skills need no tracking mode.
	pins map[string]string
	err  error
}

func newWizard(e *env, co *source.Checkout, skills []skill.Skill) *wizardScreen {
	w := &wizardScreen{env: e, co: co, skills: skills, target: e.target, spin: newSpinner()}
	w.track = newPickList(w.trackingItems(), false)
	return w
}

func (w *wizardScreen) trackingItems() []pickItem {
	if len(w.skills) == 1 {
		s := w.skills[0]
		return trackingOptions(s, func(t lock.Tracking) string { return trackingDesc(t, s) })
	}
	var items []pickItem
	for i, p := range policies {
		items = append(items, pickItem{Title: p.label, Desc: p.desc, Value: i})
	}
	return items
}

// trackingOptions lists the tracking modes available for s, described by desc.
func trackingOptions(s skill.Skill, desc func(lock.Tracking) string) []pickItem {
	var items []pickItem
	for _, t := range install.AvailableTracking(s) {
		items = append(items, pickItem{Title: trackingLabel(t), Desc: desc(t), Value: t})
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
		// The lockfile is read again when installing, so changes made by
		// others in the meantime are kept.
		in, err := w.env.app.Installer(t)
		if err != nil {
			w.err = err
			return
		}
		w.pins = w.pinsFor(in)
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

// pinsFor lists the skills that installing with in pins.
func (w *wizardScreen) pinsFor(in *install.Installer) map[string]string {
	pins := map[string]string{}
	for _, s := range w.skills {
		if p := app.PinFor(in, w.co, s, w.sel); p.Pinned {
			pins[s.Name] = p.Version
		}
	}
	return pins
}

// allPinned reports whether every skill is installed pinned, so no tracking
// mode needs to be chosen.
func (w *wizardScreen) allPinned() bool { return len(w.pins) == len(w.skills) }

// planLabel describes how s will be installed and tracked.
func (w *wizardScreen) planLabel(s skill.Skill) string {
	if v, ok := w.pins[s.Name]; ok {
		return install.PinLabel(v)
	}
	return trackingLabel(w.trackingFor(s))
}

func (w *wizardScreen) trackingFor(s skill.Skill) lock.Tracking {
	if _, ok := w.pins[s.Name]; ok {
		return lock.TrackVersion
	}
	it, _ := w.track.Current()
	if t, ok := it.Value.(lock.Tracking); ok {
		return t
	}
	return policies[it.Value.(int)].pick(s)
}

func (w *wizardScreen) run() tea.Cmd {
	w.step = stepRunning
	e, co, skills, target, agents, force, sel := w.env, w.co, w.skills, w.target, w.chosenAgents(), w.force, w.sel
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
			// The versions screen already updated the history.
			req, err := e.app.Prepare(e.ctx, in, co, s, sel, false)
			if err != nil {
				res = append(res, installResult{name: s.Name, err: err})
				continue
			}
			req.Agents, req.Tracking, req.Force = agents, tracking[i], force
			entry, err := in.Install(req)
			res = append(res, installResult{name: s.Name, entry: entry, err: err})
		}
		return installDoneMsg{results: res, saveErr: in.Lock.Save()}
	}))
}

func (w *wizardScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return tick(&w.spin, w.step == stepRunning, msg)
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
			if w.allPinned() {
				w.step = stepConfirm
			}
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
			if w.allPinned() {
				w.step = stepAgents
			}
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
			fmt.Fprintf(&b, "%-24s %s\n", s.Name, subtleStyle.Render(w.planLabel(s)))
		}
		b.WriteString("\nPress enter to install.")
	case stepRunning:
		b.WriteString(spinLine(w.spin, "installing"))
	case stepDone:
		for _, r := range w.res.results {
			if r.err != nil {
				fmt.Fprintf(&b, "%s %s: %s\n", errStyle.Render("✗"), r.name, r.err)
				continue
			}
			label := install.Describe(r.entry)
			fmt.Fprintf(&b, "%s %s %s\n", okStyle.Render("✓"), r.name, subtleStyle.Render(label))
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
	target  install.Target // target the list shows
	confirm any            // lock.Entry, install.Found or []install.Found awaiting confirmation
	msg     string
	msgErr  bool // msg reports a failure
	err     error
}

// notify shows msg, or err when it is set.
func (s *installedScreen) notify(msg string, err error) {
	s.msg, s.msgErr = msg, err != nil
	if err != nil {
		s.msg = err.Error()
	}
}

func newInstalledScreen(e *env) *installedScreen {
	l := newPickList(nil, true)
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
	var init string
	if s.in != nil && !s.in.Lock.Exists() {
		init = "i create lockfile • "
	}
	if it, ok := s.list.Current(); ok {
		if _, unmanaged := it.Value.(install.Found); unmanaged {
			return init + "space select • a select all • m manage • l license • d remove • u check updates • t change target • F favourites only • / filter"
		}
		return init + "c change tracking • l license • f favourite • d remove • u check updates • t change target • F favourites only • / filter"
	}
	return init + "u check updates • t change target • F favourites only • / filter"
}

func (s *installedScreen) reload() {
	in, err := s.env.app.Installer(s.env.target)
	s.in, s.err = in, err
	if err != nil {
		s.list.SetItems(nil)
		return
	}
	// Keep the selection when returning from another screen, but not into
	// another target, where the same names are different skills.
	checked := map[string]bool{}
	if s.target == s.env.target {
		for _, f := range s.checked() {
			checked[f.Name] = true
		}
	}
	s.target = s.env.target
	found := in.Unmanaged()
	items := make([]pickItem, 0, len(in.Lock.Skills)+len(found))
	for _, e := range in.Lock.Skills {
		items = append(items, pickItem{
			Title:   e.Name,
			Badge:   install.Describe(e),
			Desc:    withLicense(source.DisplayName(e.Source)+" • "+strings.Join(e.Agents, ", "), install.License(e).Label()),
			NoCheck: true,
			Starred: s.env.app.Config.IsFavourite(e.Source, e.Path),
			Value:   e,
		})
	}
	for _, f := range found {
		desc := strings.Join(f.Agents, ", ")
		if f.Description != "" {
			desc += " • " + f.Description
		}
		desc = withLicense(desc, f.License().Label())
		it := pickItem{Title: f.Name, Badge: "not managed", Desc: desc, Checked: checked[f.Name], Value: f}
		if _, managed := in.Lock.Get(f.Name); managed {
			// Another copy of a managed skill: removable, but not adoptable.
			it.Desc, it.NoCheck, it.Checked = "copy of the managed skill • "+desc, true, false
		}
		items = append(items, it)
	}
	s.list.SetItems(items)
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
	case []install.Found:
		for _, f := range v {
			if err := install.RemoveFound(f); err != nil {
				return err
			}
		}
	}
	return nil
}

// checked returns the unmanaged skills selected for adoption.
func (s *installedScreen) checked() []install.Found {
	var out []install.Found
	for _, it := range s.list.Checked() {
		if f, ok := it.Value.(install.Found); ok {
			out = append(out, f)
		}
	}
	return out
}

func confirmName(v any) string {
	switch v := v.(type) {
	case lock.Entry:
		return v.Name
	case install.Found:
		return v.Name
	case []install.Found:
		return fmt.Sprintf("%d skills", len(v))
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
			s.notify("", err)
			return nil
		}
		s.notify("removed "+confirmName(v), nil)
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
		if checked := s.checked(); len(checked) > 0 {
			s.confirm = checked
			return nil
		}
		if it, ok := s.list.Current(); ok {
			s.confirm = it.Value
		}
	case "m":
		if checked := s.checked(); len(checked) > 0 {
			return push(newAdoptManyScreen(s.env, s.in, checked))
		}
		if it, ok := s.list.Current(); ok {
			if f, ok := it.Value.(install.Found); ok {
				return push(newAdoptScreen(s.env, s.in, f))
			}
		}
	case "l":
		if it, ok := s.list.Current(); ok {
			if ls := licenseOfInstalled(s.env, s.in, it.Value); ls != nil {
				return push(ls)
			}
		}
	case "c":
		if it, ok := s.list.Current(); ok {
			if e, ok := it.Value.(lock.Entry); ok {
				return push(newTrackingScreen(s.env, s.in, e, func(msg string) { s.notify(msg, nil) }))
			}
		}
	case "i":
		if s.in == nil || s.in.Lock.Exists() {
			return nil
		}
		switch err := s.in.Lock.Init(); {
		case errors.Is(err, fs.ErrExist):
			// Created elsewhere since the list was loaded: show its skills,
			// or why it cannot be loaded.
			s.reload()
			s.notify("the lockfile already exists", nil)
		case err != nil:
			s.notify("", err)
		default:
			s.notify("created the lockfile", nil)
		}
	case "f":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		e, ok := it.Value.(lock.Entry)
		if !ok || e.Source == "" {
			s.notify("", errors.New("manage the skill with a source to mark it as a favourite"))
			return nil
		}
		s.notify(toggleStar(s.env, s.list, it, config.Favourite{Name: e.Name, URL: e.Source, Ref: e.Ref, Path: e.Path}))
	case "F":
		s.list.SetFavouritesOnly(!s.list.FavouritesOnly())
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
	lockfile := "lockfile: " + s.in.Lock.Path()
	if !s.in.Lock.Exists() {
		lockfile += " (not created yet)"
	}
	v := subtleStyle.Render(lockfile) + "\n\n" + s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4)
	switch c := s.confirm.(type) {
	case lock.Entry:
		v += "\n" + warnStyle.Render("Remove "+c.Name+" from all agents? (y/n)")
	case install.Found:
		v += "\n" + warnStyle.Render("Delete "+c.Name+"? It was not installed by faehigkeiten. (y/n)") +
			"\n" + subtleStyle.Render(strings.Join(c.Dirs, "\n"))
	case []install.Found:
		names := make([]string, 0, len(c))
		for _, f := range c {
			names = append(names, f.Name)
		}
		v += "\n" + warnStyle.Render(fmt.Sprintf("Delete the %d selected skills? They were not installed by faehigkeiten. (y/n)", len(c))) +
			"\n" + subtleStyle.Render(strings.Join(names, ", "))
	default:
		if n := len(s.checked()); n > 0 {
			sel := fmt.Sprintf("%d selected", n)
			if h := s.list.HiddenChecked(); h > 0 {
				sel += fmt.Sprintf(" (%d hidden by the filter)", h)
			}
			v += "\n" + subtleStyle.Render(sel+" • m manage selected • d delete selected")
		}
		switch {
		case s.msg == "":
		case s.msgErr:
			v += "\n" + errStyle.Render(s.msg)
		default:
			v += "\n" + okStyle.Render(s.msg)
		}
	}
	return v
}

// ---- tracking mode of an installed skill ----

type trackingScreen struct {
	env    *env
	in     *install.Installer
	entry  lock.Entry
	list   *pickList
	spin   spinner.Model
	busy   string
	err    error
	onDone func(msg string)
}

type (
	// choicesMsg carries the tracking modes the entry can switch to.
	choicesMsg []install.Choice
	// retrackedMsg carries the entry switched after checking upstream.
	retrackedMsg struct {
		entry lock.Entry
		err   error
	}
)

func newTrackingScreen(e *env, in *install.Installer, entry lock.Entry, onDone func(string)) *trackingScreen {
	return &trackingScreen{env: e, in: in, entry: entry, list: newPickList(nil, false), spin: newSpinner(), onDone: onDone}
}

// Init works out the choices in the background: it may hash the installed
// copy.
func (s *trackingScreen) Init() tea.Cmd {
	s.busy = "reading the installed copy"
	in, entry := s.in, s.entry
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg { return choicesMsg(in.Choices(entry)) }))
}

// setChoices lists every tracking mode; the ones the entry cannot switch to
// are disabled and say why.
func (s *trackingScreen) setChoices(choices []install.Choice) {
	items := make([]pickItem, 0, len(choices))
	for _, c := range choices {
		it := pickItem{Title: trackingLabel(c.Tracking), Value: c}
		switch {
		case c.Err != nil:
			it.Disabled, it.Desc = true, c.Err.Error()
		case c.NeedsUpstream:
			it.Desc = "No version installed; checks whether upstream declares one and offers it as an update"
		default:
			it.Desc = trackingDesc(c.Tracking, skill.Skill{Version: c.Entry.Version})
		}
		if c.Tracking == s.entry.Tracking {
			it.Badge = "current"
		}
		items = append(items, it)
	}
	s.list.SetItems(items)
	s.list.Focus(func(it pickItem) bool { return it.Value.(install.Choice).Tracking == s.entry.Tracking })
}

func (s *trackingScreen) Title() string { return s.entry.Name + " tracking" }
func (s *trackingScreen) Help() string {
	if s.busy != "" {
		return ""
	}
	return "↑/↓ move • enter select • esc back"
}

func (s *trackingScreen) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		return tick(&s.spin, s.busy != "", msg)
	case choicesMsg:
		s.busy = ""
		s.setChoices(msg)
		return nil
	case retrackedMsg:
		s.busy = ""
		if msg.err != nil {
			s.err = msg.err
			return nil
		}
		return s.apply(msg.entry)
	}
	k, ok := msg.(tea.KeyMsg)
	if !ok || s.busy != "" {
		return nil
	}
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "enter":
		it, ok := s.list.Current()
		if !ok || it.Disabled {
			return nil
		}
		c := it.Value.(install.Choice)
		switch {
		case c.Tracking == s.entry.Tracking:
			return pop
		case c.NeedsUpstream:
			s.busy, s.err = "checking upstream for a version", nil
			r, ctx := s.env.app.NewRetracker(s.in), s.env.ctx
			return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
				e, err := r.Confirm(ctx, c)
				return retrackedMsg{entry: e, err: err}
			}))
		}
		return s.apply(c.Entry)
	}
	return nil
}

// apply records e in the lockfile and returns to the installed skills.
func (s *trackingScreen) apply(e lock.Entry) tea.Cmd {
	s.in.Lock.Upsert(e)
	if err := s.in.Lock.Save(); err != nil {
		// Keep the lockfile in memory as it is on disk.
		s.in.Lock.Upsert(s.entry)
		s.err = err
		return nil
	}
	s.onDone(e.Name + " now uses " + install.Describe(e))
	return pop
}

func (s *trackingScreen) View() string {
	v := "How should " + s.entry.Name + " be checked for updates? The installed files stay as they are.\n\n" +
		s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4)
	switch {
	case s.busy != "":
		v += "\n" + spinLine(s.spin, s.busy)
	case s.err != nil:
		v += "\n" + errStyle.Render(s.err.Error())
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
		updated  []string
		unpinned []string
		errs     []error
	}
)

type updatesScreen struct {
	env  *env
	list *pickList
	spin spinner.Model
	busy string
	// confirm is the index of a locally modified pinned skill whose unpinning
	// waits for confirmation, or -1.
	confirm  int
	in       *install.Installer
	statuses []update.Status
	msg      string
	errs     []error
	err      error
}

func newUpdatesScreen(e *env) *updatesScreen {
	l := newPickList(nil, true)
	l.emptyMsg = "no tracked skills in this target"
	return &updatesScreen{env: e, list: l, spin: newSpinner(), confirm: -1}
}

func (s *updatesScreen) Title() string { return "Updates" }
func (s *updatesScreen) Help() string {
	if s.busy != "" {
		return ""
	}
	if s.confirm >= 0 {
		return "y overwrite and update • n cancel"
	}
	if it, ok := s.list.Current(); ok && s.statuses[it.Value.(int)].Entry.Pinned {
		return "space toggle • enter update selected • u unpin and update • r re-check"
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
		return tick(&s.spin, s.busy != "", msg)
	case checkedMsg:
		s.busy, s.err, s.statuses, s.in = "", msg.err, msg.statuses, msg.in
		items := make([]pickItem, 0, len(msg.statuses))
		for i, st := range msg.statuses {
			items = append(items, statusItem(i, st))
		}
		s.list.SetItems(items)
		return nil
	case appliedMsg:
		s.busy, s.errs = "", msg.errs
		switch {
		case len(msg.unpinned) > 0 && len(msg.updated) == 0:
			s.msg = "unpinned " + strings.Join(msg.unpinned, ", ")
		case len(msg.updated) > 0 || len(msg.errs) == 0:
			s.msg = fmt.Sprintf("updated %d skill(s)", len(msg.updated))
		default:
			s.msg = ""
		}
		return s.check()
	case tea.KeyMsg:
		if s.busy != "" {
			if msg.String() == "esc" {
				return pop
			}
			return nil
		}
		if s.confirm >= 0 {
			i := s.confirm
			s.confirm = -1
			if msg.String() == "y" {
				return s.unpinAt(i)
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
		case "u":
			return s.unpin()
		}
	}
	return nil
}

// statusItem is the list row of st, the i-th status.
func statusItem(i int, st update.Status) pickItem {
	it := pickItem{Title: st.Entry.Name, Value: i}
	if st.Err != nil {
		it.Disabled, it.Badge, it.Desc = true, "error", st.Err.Error()
		return it
	}
	notes := st.Notes()
	if !st.Entry.Pinned && !st.Available {
		notes = notes[1:] // "up to date" is in the description
	}
	it.Badge = strings.Join(notes, " • ")
	if st.Available {
		it.Desc = fmt.Sprintf("%s: %s → %s", st.Entry.Tracking, short(st.Current), short(st.Latest))
	} else {
		it.Desc = fmt.Sprintf("%s: %s (up to date)", st.Entry.Tracking, short(st.Current))
	}
	switch {
	case st.Entry.Pinned && st.Available:
		it.NoCheck = true
		it.Desc += " • press u to unpin and update"
	case st.Entry.Pinned:
		it.NoCheck = true
		it.Desc += " • press u to unpin"
	case !st.Available:
		it.Disabled = true
	default:
		// Modified skills are not preselected: updating overwrites the local edits.
		it.Checked = !st.Modified
	}
	if st.Available && st.Modified {
		it.Desc += " • updating overwrites your local changes"
	}
	return it
}

// unpin removes the pin of the skill under the cursor and updates it. Local
// changes are only overwritten after confirmation.
func (s *updatesScreen) unpin() tea.Cmd {
	it, ok := s.list.Current()
	if !ok || s.in == nil {
		return nil
	}
	i := it.Value.(int)
	st := s.statuses[i]
	if !st.Entry.Pinned || st.Err != nil {
		return nil
	}
	if st.UnpinChanges() && st.Modified {
		s.confirm = i
		return nil
	}
	return s.unpinAt(i)
}

func (s *updatesScreen) unpinAt(i int) tea.Cmd {
	st := s.statuses[i]
	return s.runApply("unpinning", func(in *install.Installer, res *appliedMsg) {
		switch _, updated, err := update.Unpin(in, st); {
		case err != nil:
			res.errs = append(res.errs, fmt.Errorf("%s: %w", st.Entry.Name, err))
		case updated:
			res.updated = append(res.updated, st.Entry.Name)
		default:
			res.unpinned = append(res.unpinned, st.Entry.Name)
		}
	})
}

// runApply shows busy while f changes the skills in the background, then
// saves the lockfile and reports the result.
func (s *updatesScreen) runApply(busy string, f func(*install.Installer, *appliedMsg)) tea.Cmd {
	s.busy = busy
	in := s.in
	return tea.Batch(s.spin.Tick, async(s, func() tea.Msg {
		var res appliedMsg
		f(in, &res)
		if err := in.Lock.Save(); err != nil {
			res.errs = append(res.errs, err)
		}
		return res
	}))
}

// short abbreviates a version or hash for display.
func short(v string) string {
	if v == "" {
		return "-"
	}
	return skill.ShortHash(v)
}

func (s *updatesScreen) apply() tea.Cmd {
	var picked []update.Status
	for _, it := range s.list.Checked() {
		picked = append(picked, s.statuses[it.Value.(int)])
	}
	if len(picked) == 0 || s.in == nil {
		return nil
	}
	return s.runApply("updating", func(in *install.Installer, res *appliedMsg) {
		for _, st := range picked {
			if _, err := update.Apply(in, st); err != nil {
				res.errs = append(res.errs, fmt.Errorf("%s: %w", st.Entry.Name, err))
				continue
			}
			res.updated = append(res.updated, st.Entry.Name)
		}
	})
}

func (s *updatesScreen) modifiedUpdates() int {
	n := 0
	for _, st := range s.statuses {
		if st.Updatable() && st.Modified {
			n++
		}
	}
	return n
}

func (s *updatesScreen) View() string {
	if s.busy != "" {
		return spinLine(s.spin, s.busy)
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
	if s.confirm >= 0 {
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(fmt.Sprintf(
			"%s was modified locally. Unpin and update it, overwriting your changes? (y/n)", s.statuses[s.confirm].Entry.Name)))
	}
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
