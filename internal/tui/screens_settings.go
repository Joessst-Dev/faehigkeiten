package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/Joessst-Dev/faehigkeiten/internal/agent"
	"github.com/Joessst-Dev/faehigkeiten/internal/config"
	"github.com/Joessst-Dev/faehigkeiten/internal/install"
	"github.com/Joessst-Dev/faehigkeiten/internal/source"
)

// ---- settings ----

type settingsScreen struct {
	env  *env
	list *pickList
}

const (
	settingsDefaults = "defaults"
	settingsAgents   = "agents"
	settingsRepos    = "repos"
)

func newSettingsScreen(e *env) *settingsScreen {
	s := &settingsScreen{env: e, list: newPickList(nil, false)}
	s.reload()
	return s
}

func (s *settingsScreen) reload() {
	cfg := s.env.app.Config
	defaults := "detect agents in the target"
	if ids := cfg.DefaultAgentList(); len(ids) > 0 {
		defaults = strings.Join(ids, ", ")
	}
	s.list.SetItems([]pickItem{
		{Title: "Default agents", Desc: "Preselected when installing: " + defaults, Value: settingsDefaults},
		{Title: "Agents", Desc: fmt.Sprintf("Skill directories of each agent (%s)", plural(len(cfg.AgentOverrides()), "override", "overrides")), Value: settingsAgents},
		{Title: "Repositories", Desc: fmt.Sprintf("Your own skill repositories (%s)", plural(len(cfg.RepoList()), "repository", "repositories")), Value: settingsRepos},
	})
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func (s *settingsScreen) Init() tea.Cmd   { return nil }
func (s *settingsScreen) Resume() tea.Cmd { s.reload(); return nil }
func (s *settingsScreen) Title() string   { return "Settings" }
func (s *settingsScreen) Help() string    { return "↑/↓ move • enter select" }

func (s *settingsScreen) Update(msg tea.Msg) tea.Cmd {
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
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		switch it.Value {
		case settingsDefaults:
			return push(newDefaultAgentsScreen(s.env))
		case settingsAgents:
			return push(newAgentsScreen(s.env))
		case settingsRepos:
			return push(newSettingsReposScreen(s.env))
		}
	}
	return nil
}

func (s *settingsScreen) View() string {
	v := s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-2)
	if p, err := config.Path(); err == nil {
		v += "\n\n" + subtleStyle.Render("saved to "+p)
	}
	return v
}

// commit applies change to the configuration file as it is now, so edits
// made outside the TUI are kept, and saves it. Only then does the running app
// take over the result, so nothing sees a change that was not saved.
func commit(e *env, change func(*config.Config) error) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := change(cfg); err != nil {
		return err
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	e.app.Config.Replace(cfg)
	e.app.Agents.Reload(e.app.Config)
	return nil
}

// removeRepo removes the repository with the given URL from the configuration.
func removeRepo(e *env, url string) error {
	return commit(e, func(cfg *config.Config) error {
		cfg.RemoveRepo(url)
		return nil
	})
}

// installedFor returns the skills installed for agent id in the current
// target and globally. Other projects are not checked.
func installedFor(e *env, id string) []string {
	targets := []install.Target{e.target}
	if e.target.Scope != agent.ScopeGlobal {
		if t, err := e.app.Target(agent.ScopeGlobal, ""); err == nil {
			targets = append(targets, t)
		}
	}
	var names []string
	for _, t := range targets {
		in, err := e.app.Installer(t)
		if err != nil {
			continue
		}
		for _, sk := range in.Lock.Skills {
			if slices.Contains(sk.Agents, id) {
				names = append(names, sk.Name)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// installedNote names the skills installed for agent id.
func installedNote(id string, names []string) string {
	return "Skills installed for " + id + " (" + strings.Join(names, ", ") + ")"
}

// ---- default agents ----

type defaultAgentsScreen struct {
	env  *env
	list *pickList
	err  error
}

func newDefaultAgentsScreen(e *env) *defaultAgentsScreen {
	pre := e.app.Config.DefaultAgentList()
	var items []pickItem
	for _, a := range e.app.Agents.All() {
		items = append(items, pickItem{Title: a.Name, Desc: a.ID, Checked: slices.Contains(pre, a.ID), Value: a.ID})
	}
	// Show configured IDs that are not known agents. They make installs
	// without --agent fail, so saving drops them unless they are checked.
	for _, id := range pre {
		if _, ok := e.app.Agents.Get(id); !ok {
			items = append(items, pickItem{Title: id, Desc: "not a known agent – check to keep", Badge: "unknown", Value: id})
		}
	}
	return &defaultAgentsScreen{env: e, list: newPickList(items, true)}
}

func (s *defaultAgentsScreen) Init() tea.Cmd { return nil }
func (s *defaultAgentsScreen) Title() string { return "Default agents" }
func (s *defaultAgentsScreen) Help() string {
	return "space toggle • a all • c clear • enter save"
}

func (s *defaultAgentsScreen) Update(msg tea.Msg) tea.Cmd {
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
	case "c":
		s.list.SetVisibleChecked(false)
	case "enter":
		var ids []string
		for _, it := range s.list.Checked() {
			ids = append(ids, it.Value.(string))
		}
		err := commit(s.env, func(cfg *config.Config) error {
			cfg.SetDefaultAgents(ids)
			return nil
		})
		if err != nil {
			s.err = err
			return nil
		}
		return pop
	}
	return nil
}

func (s *defaultAgentsScreen) View() string {
	v := "Agents preselected when installing. With none checked, agents found in\nthe target are preselected.\n\n" +
		s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-4)
	if s.err != nil {
		v += "\n" + errStyle.Render(s.err.Error())
	}
	return v
}

// ---- agents ----

type agentsScreen struct {
	env  *env
	list *pickList
	msg  string
	// confirm is the agent whose reset or deletion waits for a second d,
	// because skills are installed for it.
	confirm string
}

func newAgentsScreen(e *env) *agentsScreen {
	s := &agentsScreen{env: e, list: newPickList(nil, false)}
	s.reload()
	return s
}

func (s *agentsScreen) reload() {
	overrides := s.env.app.Config.AgentOverrides()
	var items []pickItem
	for _, a := range s.env.app.Agents.All() {
		badge := ""
		if _, ok := overrides[a.ID]; ok {
			badge = "override"
			if !isBuiltin(a.ID) {
				badge = "custom"
			}
		}
		items = append(items, pickItem{Title: a.Name, Desc: dirOrNone(a.ProjectDir) + " • " + dirOrNone(a.GlobalDir), Badge: badge, Value: a.ID})
	}
	s.list.SetItems(items)
}

func dirOrNone(d string) string {
	if d == "" {
		return "–"
	}
	return d
}

func builtinAgent(id string) (agent.Agent, bool) {
	builtin := agent.Builtin()
	i := slices.IndexFunc(builtin, func(a agent.Agent) bool { return a.ID == id })
	if i < 0 {
		return agent.Agent{}, false
	}
	return builtin[i], true
}

func isBuiltin(id string) bool {
	_, ok := builtinAgent(id)
	return ok
}

func (s *agentsScreen) Init() tea.Cmd   { return nil }
func (s *agentsScreen) Resume() tea.Cmd { s.reload(); return nil }
func (s *agentsScreen) Title() string   { return "Agents" }
func (s *agentsScreen) Help() string {
	return "enter edit • n new agent • d reset/delete • / filter"
}

func (s *agentsScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	confirm := s.confirm
	s.confirm = ""
	if handled, cmd := s.list.Update(k); handled {
		return cmd
	}
	switch k.String() {
	case "esc":
		return pop
	case "n":
		s.msg = ""
		return push(newAgentFormScreen(s.env, ""))
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		s.msg = ""
		return push(newAgentFormScreen(s.env, it.Value.(string)))
	case "d":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		id := it.Value.(string)
		if _, ok := s.env.app.Config.AgentOverrides()[id]; !ok {
			s.msg = id + " uses the builtin directories"
			return nil
		}
		builtin := isBuiltin(id)
		if names := installedFor(s.env, id); len(names) > 0 && confirm != id {
			s.confirm = id
			if builtin {
				s.msg = installedNote(id, names) + " stay in its current directories. Press d again to reset it."
			} else {
				s.msg = installedNote(id, names) + " can no longer be updated or uninstalled once it is deleted. Press d again to delete it."
			}
			return nil
		}
		err := commit(s.env, func(cfg *config.Config) error {
			cfg.RemoveAgent(id)
			if !builtin {
				// A deleted agent can no longer be preselected.
				cfg.SetDefaultAgents(slices.DeleteFunc(cfg.DefaultAgentList(), func(x string) bool { return x == id }))
			}
			return nil
		})
		switch {
		case err != nil:
			s.msg = err.Error()
		case builtin:
			s.msg = "reset " + id + " to the builtin directories"
		default:
			s.msg = "deleted " + id
		}
		s.reload()
	}
	return nil
}

func (s *agentsScreen) View() string {
	if s.msg == "" {
		return s.list.View(s.env.bodyWidth(), s.env.bodyHeight())
	}
	msg := warnStyle.Width(s.env.bodyWidth()).Render(s.msg)
	return s.list.View(s.env.bodyWidth(), s.env.bodyHeight()-lipgloss.Height(msg)) + "\n" + msg
}

// ---- form ----

// form is a column of text inputs with one focused field.
type form struct {
	inputs []textinput.Model
	focus  int
	err    error
}

func (f *form) Focus() tea.Cmd { return f.inputs[f.focus].Focus() }
func (f *form) Blur()          { f.inputs[f.focus].Blur() }

func (f *form) value(i int) string { return strings.TrimSpace(f.inputs[i].Value()) }

// Update moves between fields or edits the focused one.
func (f *form) Update(k tea.KeyMsg) tea.Cmd {
	d := 0
	switch k.String() {
	case "tab", "down":
		d = 1
	case "shift+tab", "up":
		d = -1
	}
	if d != 0 {
		f.Blur()
		f.focus = (f.focus + d + len(f.inputs)) % len(f.inputs)
		return f.Focus()
	}
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(k)
	return cmd
}

func (f *form) View(intro string) string {
	v := intro + "\n\n" + f.fields()
	if f.err != nil {
		v += "\n\n" + errStyle.Render(f.err.Error())
	}
	return v
}

// fields renders the inputs, one per line.
func (f *form) fields() string {
	lines := make([]string, len(f.inputs))
	for i, in := range f.inputs {
		lines[i] = in.View()
	}
	return strings.Join(lines, "\n")
}

func formInput(prompt, value, placeholder string) textinput.Model {
	in := newInput()
	in.Prompt = prompt
	in.Placeholder = placeholder
	in.SetValue(value)
	in.Width = 50
	return in
}

// ---- agent form ----

// agentID matches IDs usable in --agent lists and directory names.
var agentID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

const (
	agentFieldID = iota
	agentFieldName
	agentFieldProject
	agentFieldGlobal
)

type agentFormScreen struct {
	env *env
	// id is empty for a new agent.
	id      string
	builtin bool
	// installed lists skills installed for the agent; changing its
	// directories does not move them.
	installed []string
	form      form
}

func newAgentFormScreen(e *env, id string) *agentFormScreen {
	s := &agentFormScreen{env: e, id: id}
	o := e.app.Config.AgentOverrides()[id]
	b, builtin := builtinAgent(id)
	s.builtin = builtin
	idIn := formInput("id:          ", id, "my-agent")
	s.form.inputs = []textinput.Model{
		idIn,
		formInput("name:        ", o.Name, b.Name),
		formInput("project dir: ", o.ProjectDir, b.ProjectDir),
		formInput("global dir:  ", o.GlobalDir, b.GlobalDir),
	}
	if id != "" {
		// The ID names the agent in lockfiles, so it is fixed once created.
		s.form.inputs = s.form.inputs[agentFieldName:]
		s.installed = installedFor(e, id)
	}
	return s
}

func (s *agentFormScreen) Init() tea.Cmd { return s.form.Focus() }
func (s *agentFormScreen) Title() string {
	if s.id == "" {
		return "New agent"
	}
	return s.id
}
func (s *agentFormScreen) Help() string { return "tab next field • enter save" }

// field returns the value of the given agentField*, accounting for the
// missing ID field when editing.
func (s *agentFormScreen) field(i int) string {
	if s.id != "" {
		i--
	}
	return s.form.value(i)
}

func (s *agentFormScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc":
		return pop
	case "enter":
		if err := s.save(); err != nil {
			s.form.err = err
			return nil
		}
		return pop
	}
	return s.form.Update(k)
}

func (s *agentFormScreen) save() error {
	id := s.id
	if id == "" {
		id = s.field(agentFieldID)
		switch {
		case id == "":
			return fmt.Errorf("the id is required")
		case !agentID.MatchString(id):
			return fmt.Errorf("the id may only contain letters, digits, '.', '_' and '-'")
		}
		if _, ok := s.env.app.Agents.Get(id); ok {
			return fmt.Errorf("agent %s already exists", id)
		}
	}
	o := config.AgentOverride{
		Name:       s.field(agentFieldName),
		ProjectDir: s.field(agentFieldProject),
		GlobalDir:  s.field(agentFieldGlobal),
	}
	var err error
	if o.ProjectDir, err = cleanAgentDir(o.ProjectDir, false); err != nil {
		return fmt.Errorf("project dir: %w", err)
	}
	if o.GlobalDir, err = cleanAgentDir(o.GlobalDir, true); err != nil {
		return fmt.Errorf("global dir: %w", err)
	}
	if !s.builtin && o.ProjectDir == "" && o.GlobalDir == "" {
		return fmt.Errorf("set a project or global directory")
	}
	return commit(s.env, func(cfg *config.Config) error {
		if s.builtin && o == (config.AgentOverride{}) {
			cfg.RemoveAgent(id)
		} else {
			cfg.SetAgent(id, o)
		}
		return nil
	})
}

// cleanAgentDir checks a skills directory entered for an agent and returns it
// slash-separated. Project directories must stay inside the project; global
// ones are relative to the home directory (a leading ~/ is dropped) or
// absolute.
func cleanAgentDir(d string, global bool) (string, error) {
	if d == "" {
		return "", nil
	}
	if global {
		if rest, ok := strings.CutPrefix(d, "~/"); ok {
			d = rest
		}
		if filepath.IsAbs(d) {
			return filepath.Clean(d), nil
		}
	}
	native := filepath.FromSlash(d)
	if strings.HasPrefix(d, "~") || !filepath.IsLocal(native) {
		if global {
			return "", fmt.Errorf("%q must be relative to your home directory or absolute", d)
		}
		return "", fmt.Errorf("%q must be a path inside the project", d)
	}
	return filepath.ToSlash(filepath.Clean(native)), nil
}

func (s *agentFormScreen) View() string {
	intro := "Directories are relative to the project root and your home directory.\n"
	if s.builtin {
		intro += "Leave a field empty to keep the builtin value shown in grey."
	} else {
		intro += "Leave a directory empty if the agent does not support that scope."
	}
	if len(s.installed) > 0 {
		intro += "\n\n" + warnStyle.Width(s.env.bodyWidth()).Render(installedNote(s.id, s.installed)+
			" stay where they are when the directories change, but are then updated and uninstalled in the new ones. "+
			"Uninstall them first and reinstall them afterwards.")
	}
	return s.form.View(intro)
}

// ---- repositories (settings) ----

type settingsReposScreen struct {
	env  *env
	list *pickList
	msg  string
}

func newSettingsReposScreen(e *env) *settingsReposScreen {
	s := &settingsReposScreen{env: e, list: newPickList(nil, false)}
	s.list.emptyMsg = "no repositories yet – press n to add one"
	s.reload()
	return s
}

func (s *settingsReposScreen) reload() {
	var items []pickItem
	for _, r := range s.env.app.Config.RepoList() {
		name := r.Name
		if name == "" {
			name = source.DisplayName(r.URL)
		}
		desc := r.URL
		if r.Ref != "" {
			desc += " @ " + r.Ref
		}
		items = append(items, pickItem{Title: name, Desc: desc, Value: r})
	}
	s.list.SetItems(items)
}

func (s *settingsReposScreen) Init() tea.Cmd   { return nil }
func (s *settingsReposScreen) Resume() tea.Cmd { s.reload(); return nil }
func (s *settingsReposScreen) Title() string   { return "Repositories" }
func (s *settingsReposScreen) Help() string {
	return "enter edit • n add • d remove • / filter"
}

func (s *settingsReposScreen) Update(msg tea.Msg) tea.Cmd {
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
		s.msg = ""
		return push(newRepoFormScreen(s.env, nil))
	case "enter":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		s.msg = ""
		r := it.Value.(config.Repo)
		return push(newRepoFormScreen(s.env, &r))
	case "d":
		it, ok := s.list.Current()
		if !ok {
			return nil
		}
		if err := removeRepo(s.env, it.Value.(config.Repo).URL); err != nil {
			s.msg = err.Error()
			return nil
		}
		s.msg = "removed " + it.Title
		s.reload()
	}
	return nil
}

func (s *settingsReposScreen) View() string { return listWithMsg(s.env, s.list, s.msg) }

// ---- repository form ----

type repoFormScreen struct {
	env *env
	// orig is the repository being edited, nil for a new one.
	orig *config.Repo
	form form
}

func newRepoFormScreen(e *env, orig *config.Repo) *repoFormScreen {
	var r config.Repo
	if orig != nil {
		r = *orig
		// A name derived from the source is derived again, so it follows
		// a changed source.
		if r.Name == source.DisplayName(r.URL) {
			r.Name = ""
		}
	}
	return &repoFormScreen{env: e, orig: orig, form: form{inputs: []textinput.Model{
		formInput("source:     ", r.URL, "owner/repo, https://…, git@…, or ./local/dir"),
		formInput("branch/tag: ", r.Ref, "default branch"),
		formInput("name:       ", r.Name, "derived from the source"),
	}}}
}

func (s *repoFormScreen) Init() tea.Cmd { return s.form.Focus() }
func (s *repoFormScreen) Title() string {
	if s.orig == nil {
		return "Add repository"
	}
	return "Edit repository"
}
func (s *repoFormScreen) Help() string { return "tab next field • enter save" }

func (s *repoFormScreen) Update(msg tea.Msg) tea.Cmd {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return nil
	}
	switch k.String() {
	case "esc":
		return pop
	case "enter":
		if err := s.save(); err != nil {
			s.form.err = err
			return nil
		}
		return pop
	}
	return s.form.Update(k)
}

func (s *repoFormScreen) save() error {
	src, err := source.Normalize(s.form.value(0))
	if err != nil {
		return err
	}
	r := config.Repo{URL: src, Ref: s.form.value(1), Name: s.form.value(2)}
	if r.Name == "" {
		r.Name = source.DisplayName(src)
	}
	return commit(s.env, func(cfg *config.Config) error {
		if s.orig == nil {
			if !cfg.AddRepo(r) {
				return fmt.Errorf("%s is already configured", src)
			}
			return nil
		}
		switch err := cfg.UpdateRepo(s.orig.URL, r); {
		case errors.Is(err, config.ErrRepoNotFound):
			return fmt.Errorf("%s is no longer configured", s.orig.URL)
		case errors.Is(err, config.ErrRepoExists):
			return fmt.Errorf("%s is already configured", src)
		default:
			return err
		}
	})
}

func (s *repoFormScreen) View() string {
	return s.form.View("Skill repositories show up when browsing and searching.")
}
