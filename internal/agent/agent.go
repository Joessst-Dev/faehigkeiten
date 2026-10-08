// Package agent knows where coding agents look for skills.
package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/Joessst-Dev/faehigkeiten/internal/config"
)

// Scope selects between a project-local and a user-global installation.
type Scope string

const (
	ScopeProject Scope = "project"
	ScopeGlobal  Scope = "global"
)

// Agent is a coding agent that loads skills from a directory.
type Agent struct {
	ID   string
	Name string
	// ProjectDir is relative to the project root, slash-separated.
	ProjectDir string
	// GlobalDir is relative to the user's home directory, slash-separated.
	GlobalDir string
}

// Dir returns the skills directory of a for the given scope. projectRoot is
// required for ScopeProject, home for ScopeGlobal.
func (a Agent) Dir(scope Scope, projectRoot, home string) (string, error) {
	switch scope {
	case ScopeProject:
		if projectRoot == "" {
			return "", fmt.Errorf("agent %s: project root required", a.ID)
		}
		if a.ProjectDir == "" {
			return "", fmt.Errorf("agent %s does not support project skills", a.ID)
		}
		return filepath.Join(projectRoot, filepath.FromSlash(a.ProjectDir)), nil
	case ScopeGlobal:
		if a.GlobalDir == "" {
			return "", fmt.Errorf("agent %s does not support global skills", a.ID)
		}
		if filepath.IsAbs(a.GlobalDir) {
			return a.GlobalDir, nil
		}
		return filepath.Join(home, filepath.FromSlash(a.GlobalDir)), nil
	}
	return "", fmt.Errorf("unknown scope %q", scope)
}

// Builtin returns the agents faehigkeiten knows about out of the box.
// Several agents share the cross-agent .agents/skills convention; installs
// into the same directory are deduplicated.
func Builtin() []Agent {
	return []Agent{
		{ID: "claude-code", Name: "Claude Code", ProjectDir: ".claude/skills", GlobalDir: ".claude/skills"},
		{ID: "agents", Name: "Universal (.agents)", ProjectDir: ".agents/skills", GlobalDir: ".agents/skills"},
		{ID: "codex", Name: "OpenAI Codex", ProjectDir: ".agents/skills", GlobalDir: ".agents/skills"},
		{ID: "github-copilot", Name: "GitHub Copilot", ProjectDir: ".github/skills", GlobalDir: ".copilot/skills"},
		{ID: "cursor", Name: "Cursor", ProjectDir: ".cursor/skills", GlobalDir: ".cursor/skills"},
		{ID: "gemini-cli", Name: "Gemini CLI", ProjectDir: ".gemini/skills", GlobalDir: ".gemini/skills"},
		{ID: "opencode", Name: "OpenCode", ProjectDir: ".opencode/skills", GlobalDir: ".config/opencode/skills"},
		{ID: "windsurf", Name: "Windsurf", ProjectDir: ".windsurf/skills", GlobalDir: ".codeium/windsurf/skills"},
		{ID: "goose", Name: "Goose", ProjectDir: ".goose/skills", GlobalDir: ".config/goose/skills"},
		{ID: "kiro", Name: "Kiro CLI", ProjectDir: ".kiro/skills", GlobalDir: ".kiro/skills"},
		{ID: "roo", Name: "Roo Code", ProjectDir: ".roo/skills", GlobalDir: ".roo/skills"},
		{ID: "amp", Name: "Amp", ProjectDir: ".agents/skills", GlobalDir: ".config/agents/skills"},
	}
}

// Registry is the set of agents after applying user overrides.
type Registry struct {
	agents []Agent
}

// NewRegistry merges the builtin agents with overrides from cfg.
// Unknown override IDs add new agents.
func NewRegistry(cfg *config.Config) *Registry {
	agents := Builtin()
	if cfg != nil {
		ids := make([]string, 0, len(cfg.Agents))
		for id := range cfg.Agents {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			o := cfg.Agents[id]
			idx := -1
			for i, a := range agents {
				if a.ID == id {
					idx = i
				}
			}
			if idx < 0 {
				agents = append(agents, Agent{ID: id, Name: id})
				idx = len(agents) - 1
			}
			a := &agents[idx]
			if o.Name != "" {
				a.Name = o.Name
			}
			if o.ProjectDir != "" {
				a.ProjectDir = o.ProjectDir
			}
			if o.GlobalDir != "" {
				a.GlobalDir = o.GlobalDir
			}
		}
	}
	return &Registry{agents: agents}
}

// All returns every known agent.
func (r *Registry) All() []Agent { return append([]Agent(nil), r.agents...) }

// Get returns the agent with the given ID.
func (r *Registry) Get(id string) (Agent, bool) {
	for _, a := range r.agents {
		if a.ID == id {
			return a, true
		}
	}
	return Agent{}, false
}

// Lookup resolves ids to agents, failing on the first unknown ID.
func (r *Registry) Lookup(ids []string) ([]Agent, error) {
	out := make([]Agent, 0, len(ids))
	for _, id := range ids {
		a, ok := r.Get(id)
		if !ok {
			return nil, fmt.Errorf("unknown agent %q", id)
		}
		out = append(out, a)
	}
	return out, nil
}

// Detect returns the IDs of agents whose skills directory (or its parent
// configuration directory) already exists for the scope. It is used to
// preselect agents in the UI.
func (r *Registry) Detect(scope Scope, projectRoot, home string) []string {
	var ids []string
	for _, a := range r.agents {
		dir, err := a.Dir(scope, projectRoot, home)
		if err != nil {
			continue
		}
		if exists(dir) || exists(filepath.Dir(dir)) {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
