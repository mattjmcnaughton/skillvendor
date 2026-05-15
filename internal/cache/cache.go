// Package cache manages the local git mirror of remote skill repos at
// ~/.cache/skillvendor/<host>/<owner>/<repo>@<sha>/. Caching is keyed by
// resolved SHA, not ref, so moving refs don't trash existing snapshots.
package cache

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/paths"
)

// Cache is a local pool of checked-out git revisions.
type Cache struct {
	root string
}

// DefaultRoot returns ~/.cache/skillvendor. Honors SKILLVENDOR_HOME.
func DefaultRoot() (string, error) {
	home, err := paths.Home()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "skillvendor"), nil
}

func New(root string) *Cache { return &Cache{root: root} }

func (c *Cache) Root() string { return c.root }

// cloneURL converts a repo identifier like "github.com/foo/bar" into a clone
// URL. Inputs that already include a scheme (https://, ssh://, file://) or
// look like an SSH-style path (git@host:owner/repo) are returned as-is.
func cloneURL(repo string) string {
	if strings.Contains(repo, "://") || strings.HasPrefix(repo, "git@") {
		return repo
	}
	return "https://" + strings.TrimSuffix(repo, ".git") + ".git"
}

// repoSubdir maps a repo identifier to a stable subdirectory name within the
// cache: "github.com/foo/bar" → "github.com/foo/bar". URLs with schemes are
// normalized so the on-disk layout stays predictable.
func repoSubdir(repo string) string {
	r := repo
	r = strings.TrimPrefix(r, "https://")
	r = strings.TrimPrefix(r, "http://")
	r = strings.TrimPrefix(r, "ssh://")
	r = strings.TrimPrefix(r, "file://")
	if strings.HasPrefix(r, "git@") {
		r = strings.TrimPrefix(r, "git@")
		r = strings.Replace(r, ":", "/", 1)
	}
	r = strings.TrimSuffix(r, ".git")
	r = strings.TrimPrefix(r, "/")
	return r
}

// PathFor returns the worktree directory for a given (repo, sha), without
// requiring it to exist on disk.
func (c *Cache) PathFor(repo, sha string) string {
	return filepath.Join(c.root, repoSubdir(repo)+"@"+sha)
}

// ResolveRef returns the commit SHA that the remote currently resolves <ref>
// to. Annotated tags resolve to the commit they point at.
func (c *Cache) ResolveRef(repo, ref string) (string, error) {
	if ref == "" {
		return "", errors.New("ref is required")
	}
	url := cloneURL(repo)
	out, err := exec.Command("git", "ls-remote", url, ref).Output()
	if err != nil {
		return "", fmt.Errorf("ls-remote %s %s: %w", url, ref, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "", fmt.Errorf("ref %q not found in %s", ref, repo)
	}
	// Prefer ^{} (annotated tag dereference) if present.
	pick := lines[0]
	for _, l := range lines {
		if strings.HasSuffix(l, "^{}") {
			pick = l
			break
		}
	}
	fields := strings.Fields(pick)
	if len(fields) < 1 || len(fields[0]) < 7 {
		return "", fmt.Errorf("unexpected ls-remote output: %q", out)
	}
	return fields[0], nil
}

// Fetch ensures the (repo, sha) worktree exists locally and returns its path.
// Idempotent: if the cache entry is already present, no network calls happen.
func (c *Cache) Fetch(repo, sha string) (string, error) {
	if sha == "" {
		return "", errors.New("sha is required")
	}
	dst := c.PathFor(repo, sha)
	if info, err := os.Stat(dst); err == nil && info.IsDir() {
		return dst, nil
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}

	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".fetch-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	url := cloneURL(repo)
	// Full clone (not shallow) so we can check out an arbitrary SHA, which
	// shallow clones can't always resolve when the SHA is older than the
	// remote default depth.
	if err := run("git", "clone", "--quiet", url, tmp); err != nil {
		return "", fmt.Errorf("clone %s: %w", url, err)
	}
	if err := runIn(tmp, "git", "checkout", "--quiet", sha); err != nil {
		return "", fmt.Errorf("checkout %s in %s: %w", sha, repo, err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return "", err
	}
	return dst, nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runIn(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
