// Package config loads and stores user configuration and resolves the
// directories faehigkeiten keeps its state in.
package config

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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

// Config is the persisted user configuration.
type Config struct {
	DefaultAgents []string
	Agents        map[string]AgentOverride

	// repos may change while background tasks read it, so it is only
	// reached through AddRepo, RemoveRepo and RepoList.
	mu    sync.Mutex
	repos []Repo
}

// fileConfig is the on-disk form of Config.
type fileConfig struct {
	Repos         []Repo                   `yaml:"repos,omitempty"`
	DefaultAgents []string                 `yaml:"default_agents,omitempty"`
	Agents        map[string]AgentOverride `yaml:"agents,omitempty"`
}

// MarshalYAML implements yaml.Marshaler.
func (c *Config) MarshalYAML() (any, error) {
	return fileConfig{Repos: c.RepoList(), DefaultAgents: c.DefaultAgents, Agents: c.Agents}, nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (c *Config) UnmarshalYAML(n *yaml.Node) error {
	var f fileConfig
	if err := n.Decode(&f); err != nil {
		return err
	}
	c.DefaultAgents, c.Agents = f.DefaultAgents, f.Agents
	c.mu.Lock()
	c.repos = f.Repos
	c.mu.Unlock()
	return nil
}

// Dir returns the configuration directory.
func Dir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
}

// CacheDir returns the cache directory used for cloned repositories and indexes.
func CacheDir() (string, error) {
	if d := os.Getenv(EnvCacheDir); d != "" {
		return d, nil
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, appName), nil
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
	return LoadFile(p)
}

// LoadFile reads the config from p; a missing file yields an empty config.
func LoadFile(p string) (*Config, error) {
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
	return c.SaveFile(p)
}

// SaveFile writes the config to p.
func (c *Config) SaveFile(p string) error {
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
