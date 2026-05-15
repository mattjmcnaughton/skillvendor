// Package symlink installs and removes symlinks from cached skill directories
// into the user's Claude / Codex skill directories. Existing non-symlink
// entries with the same name are treated as user-owned and never overwritten.
package symlink

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/paths"
)

// Installer manages symlinks under each target directory. A symlink is
// considered "owned" by this installer when its target resolves under
// cacheRoot — anything else is user-owned and left alone.
type Installer struct {
	targets   []string
	cacheRoot string
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

func New(targets []string, cacheRoot string) *Installer {
	return &Installer{targets: targets, cacheRoot: cacheRoot}
}

func (i *Installer) Targets() []string { return i.targets }

// Install creates a symlink named `skill` in every target dir pointing at src.
//
// Errors loudly if a target already has a non-symlink entry, or a symlink
// pointing outside the cache, with that name. Replaces stale managed
// symlinks in place.
func (i *Installer) Install(skill, src string) error {
	if skill == "" {
		return errors.New("skill name is empty")
	}
	if src == "" {
		return errors.New("src path is empty")
	}
	for _, t := range i.targets {
		if err := os.MkdirAll(t, 0o755); err != nil {
			return err
		}
		link := filepath.Join(t, skill)
		if err := i.installOne(link, src); err != nil {
			return fmt.Errorf("%s: %w", link, err)
		}
	}
	return nil
}

func (i *Installer) installOne(link, src string) error {
	info, err := os.Lstat(link)
	if err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("refusing to overwrite existing non-symlink at %s", link)
		}
		target, rerr := os.Readlink(link)
		if rerr != nil {
			return rerr
		}
		if !i.owns(target) {
			return fmt.Errorf("refusing to overwrite symlink at %s (points outside cache: %s)", link, target)
		}
		if target == src {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Symlink(src, link)
}

// Remove drops the symlink for `skill` from every target, only when it's a
// managed symlink. Returns nil if nothing was there or it wasn't managed.
func (i *Installer) Remove(skill string) error {
	for _, t := range i.targets {
		link := filepath.Join(t, skill)
		info, err := os.Lstat(link)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		target, rerr := os.Readlink(link)
		if rerr != nil {
			return rerr
		}
		if !i.owns(target) {
			continue
		}
		if err := os.Remove(link); err != nil {
			return err
		}
	}
	return nil
}

// IsManaged reports whether `skill` is currently installed as a managed
// symlink in every target. Useful for `list`.
func (i *Installer) IsManaged(skill string) bool {
	for _, t := range i.targets {
		link := filepath.Join(t, skill)
		info, err := os.Lstat(link)
		if err != nil {
			return false
		}
		if info.Mode()&os.ModeSymlink == 0 {
			return false
		}
		target, err := os.Readlink(link)
		if err != nil || !i.owns(target) {
			return false
		}
	}
	return true
}

func (i *Installer) owns(target string) bool {
	if i.cacheRoot == "" {
		return false
	}
	abs, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	root, err := filepath.Abs(i.cacheRoot)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
