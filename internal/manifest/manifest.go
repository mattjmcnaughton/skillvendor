// Package manifest reads, mutates, and writes the user's skills.yaml manifest
// at ~/.config/skillvendor/skills.yaml.
package manifest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/paths"
	"gopkg.in/yaml.v3"
)

const DefaultRef = "main"

// Entry describes a single remote source of skills.
//
// Path points to a directory CONTAINING skills (each immediate subdir with a
// SKILL.md is one installable skill). An empty Path means the repo root.
// Include and Exclude are mutually exclusive filters on subdir basenames.
type Entry struct {
	Repo    string   `yaml:"repo"`
	Ref     string   `yaml:"ref,omitempty"`
	Path    string   `yaml:"path,omitempty"`
	Include []string `yaml:"include,omitempty"`
	Exclude []string `yaml:"exclude,omitempty"`
}

// Key identifies an entry uniquely within the manifest by repo + path. Two
// entries with the same repo but different paths are independent.
func (e Entry) Key() string {
	if e.Path == "" {
		return e.Repo
	}
	return e.Repo + "#" + e.Path
}

func (e Entry) Validate() error {
	if e.Repo == "" {
		return errors.New("repo is required")
	}
	if len(e.Include) > 0 && len(e.Exclude) > 0 {
		return fmt.Errorf("%s: include and exclude are mutually exclusive", e.Key())
	}
	return nil
}

type Manifest struct {
	// Targets are directories that managed skills are symlinked into. When
	// empty, DefaultTargets is used. `~` and `~/...` are expanded against
	// paths.Home (so SKILLVENDOR_HOME redirects them too); absolute paths
	// pass through unchanged.
	Targets []string `yaml:"targets,omitempty"`
	Skills  []Entry  `yaml:"skills"`

	path string
}

// DefaultTargets returns ~/.claude/skills and ~/.codex/skills. Honors SKILLVENDOR_HOME.
func DefaultTargets() ([]string, error) {
	home, err := paths.Home()
	if err != nil {
		return nil, err
	}
	return []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
	}, nil
}

// ResolvedTargets returns the target directories with `~` expanded. If the
// manifest declares no targets, the defaults are used.
func (m *Manifest) ResolvedTargets() ([]string, error) {
	if len(m.Targets) == 0 {
		return DefaultTargets()
	}
	home, err := paths.Home()
	if err != nil {
		return nil, err
	}
	out := make([]string, len(m.Targets))
	for i, t := range m.Targets {
		out[i] = expandHome(t, home)
	}
	return out, nil
}

func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(home, p[2:])
	}
	return p
}

// DefaultPath returns ~/.config/skillvendor/skills.yaml. Honors SKILLVENDOR_HOME.
func DefaultPath() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "skillvendor", "skills.yaml"), nil
}

// Load reads the manifest at path. A missing file returns an empty manifest
// (not an error) so first-time users can `add` without `init`.
func Load(path string) (*Manifest, error) {
	m := &Manifest{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i, e := range m.Skills {
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("manifest entry %d: %w", i, err)
		}
	}
	return m, nil
}

// Save writes the manifest atomically (write-temp-then-rename).
func (m *Manifest) Save() error {
	if m.path == "" {
		return errors.New("manifest path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o755); err != nil {
		return err
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".skills.*.yaml")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), m.path)
}

// Path returns the on-disk location this manifest was loaded from / will save to.
func (m *Manifest) Path() string { return m.path }

// Find returns the index of the entry matching repo (and optional path), or -1.
func (m *Manifest) Find(repo, path string) int {
	for i, e := range m.Skills {
		if e.Repo == repo && e.Path == path {
			return i
		}
	}
	return -1
}

// Upsert inserts or replaces an entry. Identity is (repo, path).
func (m *Manifest) Upsert(e Entry) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if e.Ref == "" {
		e.Ref = DefaultRef
	}
	if i := m.Find(e.Repo, e.Path); i >= 0 {
		m.Skills[i] = e
		return nil
	}
	m.Skills = append(m.Skills, e)
	return nil
}

// Remove deletes the entry matching repo + path. Returns false if not found.
func (m *Manifest) Remove(repo, path string) bool {
	i := m.Find(repo, path)
	if i < 0 {
		return false
	}
	m.Skills = append(m.Skills[:i], m.Skills[i+1:]...)
	return true
}
