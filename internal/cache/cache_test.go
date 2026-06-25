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

// TestResolveRefPrefersHeads guards against a bug where `git ls-remote <url>
// main` does suffix-style matching and returns multiple lines (e.g. a sibling
// branch `refs/heads/dev/main` and a remote-tracking `refs/remotes/matt/main`
// alongside the real `refs/heads/main`). The previous code blindly took
// lines[0], which is the alphabetically-first match and not necessarily the
// branch the user meant.
func TestResolveRefPrefersHeads(t *testing.T) {
	repoDir, oldSHA := fixtureRepo(t)
	// Advance refs/heads/main with a second commit; decoys (planted below)
	// stay pinned at oldSHA. The SHA gap is what makes a wrong pick observable.
	if err := os.WriteFile(filepath.Join(repoDir, "second.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, repoDir, "git", "add", ".")
	mustRun(t, repoDir, "git", "commit", "--quiet", "-m", "second")
	out, err := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatalf("rev-parse: %v", err)
	}
	newHead := strings.TrimSpace(string(out))
	// refs/heads/dev/main sorts before refs/heads/main, so ls-remote returns
	// it first; this is what made lines[0] pick the wrong SHA in practice.
	mustRun(t, repoDir, "git", "update-ref", "refs/heads/dev/main", oldSHA)
	// refs/remotes/matt/main is the other variant from the original bug
	// report — also matched by the unqualified `main` pattern.
	mustRun(t, repoDir, "git", "update-ref", "refs/remotes/matt/main", oldSHA)

	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file://"+repoDir, "main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != newHead {
		t.Errorf("resolved %s, want %s (refs/heads/main)", resolved, newHead)
	}
}

// TestResolveRefQualifiedPassThrough ensures that a ref containing "/" is
// passed to ls-remote unchanged, so callers can pin exotic refs like
// refs/pull/<n>/head that wouldn't be found under refs/heads or refs/tags.
func TestResolveRefQualifiedPassThrough(t *testing.T) {
	repoDir, headSHA := fixtureRepo(t)
	mustRun(t, repoDir, "git", "update-ref", "refs/pull/1/head", headSHA)

	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file://"+repoDir, "refs/pull/1/head")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != headSHA {
		t.Errorf("resolved %s, want %s", resolved, headSHA)
	}
}

// TestResolveRefFullSHA ensures a full-length commit SHA is treated as a pin
// and returned verbatim without any remote lookup. The repo URL points
// nowhere, so any ls-remote attempt would error — a clean return proves the
// short-circuit.
func TestResolveRefFullSHA(t *testing.T) {
	sha := "4f1a2b3c4d5e6f7890abcdef1234567890abcdef" // 40 hex chars
	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file:///nonexistent/repo", sha)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != sha {
		t.Errorf("resolved %s, want %s", resolved, sha)
	}
}

// TestResolveRefSHA256 ensures a full-length SHA-256 object id (64 hex chars)
// is also inferred as a pin and returned verbatim without a remote lookup.
func TestResolveRefSHA256(t *testing.T) {
	sha := strings.Repeat("ab", 32) // 64 hex chars
	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file:///nonexistent/repo", sha)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != sha {
		t.Errorf("resolved %s, want %s", resolved, sha)
	}
}

// TestResolveRefNonHexNotPin guards the inference heuristic against false
// positives: a 40-character string that isn't valid hex is a branch/tag name,
// not a commit, and must still resolve through ls-remote.
func TestResolveRefNonHexNotPin(t *testing.T) {
	repoDir, headSHA := fixtureRepo(t)
	branch := strings.Repeat("g", 40) // 40 chars, but 'g' isn't hex
	mustRun(t, repoDir, "git", "branch", branch)

	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file://"+repoDir, branch)
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != headSHA {
		t.Errorf("resolved %s, want %s (branch resolved via ls-remote)", resolved, headSHA)
	}
}

// TestResolveRefForcedSHAInvalid ensures a "sha:" prefix wrapping a non-hex
// value is a hard error rather than being passed to ls-remote or checkout.
func TestResolveRefForcedSHAInvalid(t *testing.T) {
	c := New(t.TempDir())
	if _, err := c.ResolveRef("file:///nonexistent/repo", "sha:not-hex"); err == nil {
		t.Error("expected error for non-hex sha pin")
	}
}

// TestResolveRefForcedSHA ensures a "sha:" prefix forces commit-pin treatment
// even for an abbreviated SHA we couldn't safely infer on our own. The prefix
// is stripped from the returned value, and no remote lookup happens.
func TestResolveRefForcedSHA(t *testing.T) {
	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file:///nonexistent/repo", "sha:4f1a2b3")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != "4f1a2b3" {
		t.Errorf("resolved %q, want %q", resolved, "4f1a2b3")
	}
}

// TestResolveRefAnnotatedTag ensures an unqualified tag name still
// dereferences to the commit it points at, not the tag object's own SHA.
func TestResolveRefAnnotatedTag(t *testing.T) {
	repoDir, headSHA := fixtureRepo(t)
	mustRun(t, repoDir, "git", "tag", "-a", "v1.0.0", "-m", "release")

	c := New(t.TempDir())
	resolved, err := c.ResolveRef("file://"+repoDir, "v1.0.0")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if resolved != headSHA {
		t.Errorf("resolved %s, want %s (commit SHA, not tag object)", resolved, headSHA)
	}
}
