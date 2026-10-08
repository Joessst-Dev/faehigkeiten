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
	Repos         []Repo                   `yaml:"repos,omitempty"`
	DefaultAgents []string                 `yaml:"default_agents,omitempty"`
	Agents        map[string]AgentOverride `yaml:"agents,omitempty"`
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
	if slices.ContainsFunc(c.Repos, func(x Repo) bool { return x.URL == r.URL }) {
		return false
	}
	c.Repos = append(c.Repos, r)
	return true
}

// RemoveRepo removes the repo with the given URL and reports whether it existed.
func (c *Config) RemoveRepo(url string) bool {
	n := len(c.Repos)
	c.Repos = slices.DeleteFunc(c.Repos, func(x Repo) bool { return x.URL == url })
	return len(c.Repos) != n
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
