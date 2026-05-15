package cache

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCloneURL(t *testing.T) {
	cases := map[string]string{
		"github.com/foo/bar":       "https://github.com/foo/bar.git",
		"github.com/foo/bar.git":   "https://github.com/foo/bar.git",
		"https://example.com/x":    "https://example.com/x",
		"git@github.com:foo/bar":   "git@github.com:foo/bar",
		"file:///tmp/repo":         "file:///tmp/repo",
	}
	for in, want := range cases {
		if got := cloneURL(in); got != want {
			t.Errorf("cloneURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoSubdir(t *testing.T) {
	cases := map[string]string{
		"github.com/foo/bar":          "github.com/foo/bar",
		"https://github.com/foo/bar":  "github.com/foo/bar",
		"https://github.com/foo/bar.git": "github.com/foo/bar",
		"git@github.com:foo/bar.git":  "github.com/foo/bar",
		"file:///tmp/repo":            "tmp/repo",
	}
	for in, want := range cases {
		if got := repoSubdir(in); got != want {
			t.Errorf("repoSubdir(%q) = %q, want %q", in, got, want)
		}
	}
}

// fixtureRepo creates a local git repo with a single commit and returns its
// path and the SHA of HEAD. The test is skipped if `git` isn't available.
func fixtureRepo(t *testing.T) (path, sha string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustRun(t, dir, "git", "init", "--quiet", "-b", "main")
	mustRun(t, dir, "git", "config", "user.email", "test@example.com")
	mustRun(t, dir, "git", "config", "user.name", "Test")
	mustRun(t, dir, "git", "config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, dir, "git", "add", ".")
	mustRun(t, dir, "git", "commit", "--quiet", "-m", "init")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	return dir, strings.TrimSpace(string(out))
}

func mustRun(t *testing.T, dir, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func TestResolveAndFetch(t *testing.T) {
	repoDir, headSHA := fixtureRepo(t)
	cacheRoot := t.TempDir()
	c := New(cacheRoot)
	repo := "file://" + repoDir

	resolved, err := c.ResolveRef(repo, "main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != headSHA {
		t.Errorf("resolved %s, want %s", resolved, headSHA)
	}

	path, err := c.Fetch(repo, resolved)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "README.md")); err != nil {
		t.Errorf("worktree missing README: %v", err)
	}

	// Idempotent: second fetch returns same path without re-cloning.
	path2, err := c.Fetch(repo, resolved)
	if err != nil {
		t.Fatalf("Fetch (second): %v", err)
	}
	if path2 != path {
		t.Errorf("second Fetch returned different path: %s vs %s", path2, path)
	}
}

func TestResolveMissingRef(t *testing.T) {
	repoDir, _ := fixtureRepo(t)
	c := New(t.TempDir())
	if _, err := c.ResolveRef("file://"+repoDir, "does-not-exist"); err == nil {
		t.Error("expected error for missing ref")
	}
}
