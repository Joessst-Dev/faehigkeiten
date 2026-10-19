// Package config loads and stores user configuration and resolves the
// directories faehigkeiten keeps its state in.
package config

import (
	"bytes"
	"cmp"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

const appName = "faehigkeiten"

// Environment variables overriding the default state locations.
const (
	EnvConfigDir = "FAEHIGKEITEN_CONFIG_DIR"
	EnvCacheDir  = "FAEHIGKEITEN_CACHE_DIR"
)

// Repo is a user-added skill repository.
type Repo struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Ref  string `yaml:"ref,omitempty"`
}

// AgentOverride changes or adds an agent target.
type AgentOverride struct {
	Name       string `yaml:"name,omitempty"`
	ProjectDir string `yaml:"project_dir,omitempty"`
	GlobalDir  string `yaml:"global_dir,omitempty"`
}

// Favourite is a skill the user marked, identified by its source URL and its
// path in the source.
type Favourite struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
	Ref  string `yaml:"ref,omitempty"`
	Path string `yaml:"path"`
}

// FromSource reports whether f is a skill of the source url. URLs are
// compared ignoring case, since GitHub owner and repository names are.
func (f Favourite) FromSource(url string) bool {
	return strings.EqualFold(f.URL, url)
}

// Is reports whether f is the skill at path in the source url.
func (f Favourite) Is(url, path string) bool {
	return f.FromSource(url) && f.Path == path
}

// SortFavourites sorts favourites by name, then by source and path.
func SortFavourites(favs []Favourite) {
	slices.SortFunc(favs, func(a, b Favourite) int {
		return cmp.Or(
			strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)),
			strings.Compare(strings.ToLower(a.URL), strings.ToLower(b.URL)),
			strings.Compare(a.Path, b.Path),
		)
	})
}

// Config is the persisted user configuration. Its fields may change while
// background tasks read them, so they are only reached through methods that
// hold mu.
type Config struct {
	mu            sync.Mutex
	repos         []Repo
	defaultAgents []string
	agents        map[string]AgentOverride
	favourites    []Favourite
}

// fileConfig is the on-disk form of Config.
type fileConfig struct {
	Repos         []Repo                   `yaml:"repos,omitempty"`
	DefaultAgents []string                 `yaml:"default_agents,omitempty"`
	Agents        map[string]AgentOverride `yaml:"agents,omitempty"`
	Favourites    []Favourite              `yaml:"favourites,omitempty"`
}

// MarshalYAML implements yaml.Marshaler.
func (c *Config) MarshalYAML() (any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fileConfig{Repos: slices.Clone(c.repos), DefaultAgents: slices.Clone(c.defaultAgents), Agents: maps.Clone(c.agents), Favourites: slices.Clone(c.favourites)}, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Config) UnmarshalYAML(n *yaml.Node) error {
	var f fileConfig
	if err := n.Decode(&f); err != nil {
		return err
	}
	c.mu.Lock()
	c.repos, c.defaultAgents, c.agents, c.favourites = f.Repos, f.DefaultAgents, f.Agents, f.Favourites
	c.mu.Unlock()
	return nil
}

// Dir returns the configuration directory.
func Dir() (string, error) {
	return stateDir(EnvConfigDir, os.UserConfigDir)
}

// CacheDir returns the cache directory used for cloned repositories and indexes.
func CacheDir() (string, error) {
	return stateDir(EnvCacheDir, os.UserCacheDir)
}

// stateDir returns the directory set in the environment variable env, or the
// application's directory below the one base returns.
func stateDir(env string, base func() (string, error)) (string, error) {
	if d := os.Getenv(env); d != "" {
		return d, nil
	}
	dir, err := base()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, appName), nil
}

// Path returns the location of the config file.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// Load reads the config file; a missing file yields an empty config.
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	return loadFile(p)
}

// loadFile reads the config from p; a missing file yields an empty config.
func loadFile(p string) (*Config, error) {
	cfg := &Config{}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// Save writes the config file.
func (c *Config) Save() error {
	p, err := Path()
	if err != nil {
		return err
	}
	return c.saveFile(p)
}

// saveFile writes the config to p.
func (c *Config) saveFile(p string) error {
	data, err := marshal(c)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// AddRepo adds r unless a repo with the same URL exists. It reports whether r was added.
func (c *Config) AddRepo(r Repo) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.ContainsFunc(c.repos, func(x Repo) bool { return x.URL == r.URL }) {
		return false
	}
	c.repos = append(c.repos, r)
	return true
}

// RemoveRepo removes the repo with the given URL and reports whether it existed.
func (c *Config) RemoveRepo(url string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.repos)
	c.repos = slices.DeleteFunc(c.repos, func(x Repo) bool { return x.URL == url })
	return len(c.repos) != n
}

// RepoList returns a copy of the user's repositories. It may be called while
// another goroutine adds or removes one.
func (c *Config) RepoList() []Repo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.repos)
}

// Errors returned by UpdateRepo.
var (
	ErrRepoNotFound = errors.New("repository is not configured")
	ErrRepoExists   = errors.New("repository is already configured")
)

// UpdateRepo replaces the repo with URL oldURL by r. It fails with
// ErrRepoNotFound when no such repo exists and with ErrRepoExists when
// another repo already uses r's URL.
func (c *Config) UpdateRepo(oldURL string, r Repo) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := slices.IndexFunc(c.repos, func(x Repo) bool { return x.URL == oldURL })
	if i < 0 {
		return ErrRepoNotFound
	}
	if r.URL != oldURL && slices.ContainsFunc(c.repos, func(x Repo) bool { return x.URL == r.URL }) {
		return ErrRepoExists
	}
	c.repos[i] = r
	return nil
}

// DefaultAgentList returns a copy of the agents preselected for installs.
func (c *Config) DefaultAgentList() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.defaultAgents)
}

// SetDefaultAgents replaces the agents preselected for installs. An empty
// list means they are detected instead.
func (c *Config) SetDefaultAgents(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(ids) == 0 {
		c.defaultAgents = nil
		return
	}
	c.defaultAgents = slices.Clone(ids)
}

// AgentOverrides returns a copy of the agent overrides keyed by agent ID.
func (c *Config) AgentOverrides() map[string]AgentOverride {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.agents)
}

// SetAgent stores the override for the agent with the given ID.
func (c *Config) SetAgent(id string, o AgentOverride) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agents == nil {
		c.agents = map[string]AgentOverride{}
	}
	c.agents[id] = o
}

// RemoveAgent removes the override for id and reports whether it existed.
func (c *Config) RemoveAgent(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.agents[id]; !ok {
		return false
	}
	delete(c.agents, id)
	if len(c.agents) == 0 {
		c.agents = nil
	}
	return true
}

// FavouriteList returns a copy of the user's favourite skills.
func (c *Config) FavouriteList() []Favourite {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.favourites)
}

// IsFavourite reports whether the skill at path in the source url is a favourite.
func (c *Config) IsFavourite(url, path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.ContainsFunc(c.favourites, func(x Favourite) bool { return x.Is(url, path) })
}

// AddFavourite adds f unless a favourite with the same URL and path exists.
// It reports whether f was added.
func (c *Config) AddFavourite(f Favourite) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if slices.ContainsFunc(c.favourites, func(x Favourite) bool { return x.Is(f.URL, f.Path) }) {
		return false
	}
	c.favourites = append(c.favourites, f)
	return true
}

// RemoveFavourite removes the favourite with the given URL and path and
// reports whether it existed.
func (c *Config) RemoveFavourite(url, path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(c.favourites)
	c.favourites = slices.DeleteFunc(c.favourites, func(x Favourite) bool { return x.Is(url, path) })
	if len(c.favourites) == 0 {
		c.favourites = nil
	}
	return len(c.favourites) != n
}

// Replace sets all settings of c to those of o, for example after o was
// loaded from the file and changed.
func (c *Config) Replace(o *Config) {
	repos, defaults, agents, favs := o.RepoList(), o.DefaultAgentList(), o.AgentOverrides(), o.FavouriteList()
	c.mu.Lock()
	c.repos, c.defaultAgents, c.agents, c.favourites = repos, defaults, agents, favs
	c.mu.Unlock()
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
