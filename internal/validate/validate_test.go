package validate

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mattjmcnaughton/skillvendor/internal/config"
)

func TestHookHashTrims(t *testing.T) {
	a := HookHash("  ~/hook.sh \n")
	b := HookHash("~/hook.sh")
	if a != b {
		t.Errorf("hash should ignore surrounding whitespace: %s != %s", a, b)
	}
	if len(a) != 64 {
		t.Errorf("expected 64 hex chars, got %d", len(a))
	}
	if HookHash("other") == a {
		t.Error("different commands should hash differently")
	}
}

func TestCached(t *testing.T) {
	prev := config.LockEntry{Validated: map[string]config.Validation{"pdf": {Tree: "t1", Hook: "h1"}}}
	cases := []struct {
		skill, tree, hook string
		want              bool
	}{
		{"pdf", "t1", "h1", true},
		{"pdf", "t2", "h1", false},
		{"pdf", "t1", "h2", false},
		{"docx", "t1", "h1", false},
	}
	for _, tc := range cases {
		if got := Cached(prev, tc.skill, tc.tree, tc.hook); got != tc.want {
			t.Errorf("Cached(%s,%s,%s) = %v, want %v", tc.skill, tc.tree, tc.hook, got, tc.want)
		}
	}
	if Cached(config.LockEntry{}, "pdf", "t1", "h1") {
		t.Error("empty entry should never be cached")
	}
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestTreeHash(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "--quiet", "-b", "main")
	mustGit(t, dir, "config", "user.email", "t@example.com")
	mustGit(t, dir, "config", "user.name", "T")
	mustGit(t, dir, "config", "commit.gpgsign", "false")
	for _, p := range []string{"skills/pdf/SKILL.md", "root/SKILL.md"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("v1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "--quiet", "-m", "one")

	nested, err := TreeHash(dir, "skills", "pdf")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustGit(t, dir, "rev-parse", "HEAD:skills/pdf"); nested != want {
		t.Errorf("nested = %s, want %s", nested, want)
	}
	root, err := TreeHash(dir, "", "root")
	if err != nil {
		t.Fatal(err)
	}
	if want := mustGit(t, dir, "rev-parse", "HEAD:root"); root != want {
		t.Errorf("root = %s, want %s", root, want)
	}
	// Identical content in two dirs hashes to the same tree.
	if root != nested {
		t.Errorf("identical trees should hash equal: %s != %s", root, nested)
	}
	for _, dirForm := range []string{"./skills", "skills/"} {
		if got, err := TreeHash(dir, dirForm, "pdf"); err != nil || got != nested {
			t.Errorf("TreeHash(%q) = %s, %v", dirForm, got, err)
		}
	}

	// Changing the skill changes its hash; the untouched one is stable.
	if err := os.WriteFile(filepath.Join(dir, "skills/pdf/SKILL.md"), []byte("v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, "commit", "--quiet", "-am", "two")
	changed, _ := TreeHash(dir, "skills", "pdf")
	same, _ := TreeHash(dir, "", "root")
	if changed == nested || same != root {
		t.Errorf("changed=%s same=%s", changed, same)
	}
	if _, err := TreeHash(dir, "", "missing"); err == nil {
		t.Error("expected error for a missing skill")
	}
}

func TestRun(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "env.txt")
	env := Env{Event: "update", Skill: "pdf", SkillDir: dir, PrevSkillDir: "/prev", Repo: "r", Ref: "main", SHA: "1", PrevSHA: "0"}

	// The hook inherits our environment: $OUT is visible to it.
	t.Setenv("OUT", log)
	if err := Run(`pwd > "$OUT"; env | grep ^SKILLVENDOR_ | sort >> "$OUT"`, env); err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(dir)
	want := real + "\n" +
		"SKILLVENDOR_EVENT=update\nSKILLVENDOR_PREV_SHA=0\nSKILLVENDOR_PREV_SKILL_DIR=/prev\n" +
		"SKILLVENDOR_REF=main\nSKILLVENDOR_REPO=r\nSKILLVENDOR_SHA=1\nSKILLVENDOR_SKILL=pdf\n" +
		"SKILLVENDOR_SKILL_DIR=" + dir + "\n"
	if string(body) != want {
		t.Errorf("hook saw:\n%s\nwant:\n%s", body, want)
	}

	err = Run("exit 3", env)
	if !errors.Is(err, ErrRejected) || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("nonzero exit should wrap ErrRejected, got %v", err)
	}
	if err := Run("exit 0", Env{SkillDir: filepath.Join(dir, "missing")}); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("unrunnable hook should be a plain error, got %v", err)
	}
}
