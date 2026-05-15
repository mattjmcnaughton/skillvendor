package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles skillvendor into a temp dir and returns its path.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "skillvendor")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return bin
}

// fixtureSkillsRepo creates a local git repo containing two skills under
// `document-skills/` and returns its absolute path.
func fixtureSkillsRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	must := func(args ...string) {
		c := exec.Command(args[0], args[1:]...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	must("git", "init", "--quiet", "-b", "main")
	must("git", "config", "user.email", "test@example.com")
	must("git", "config", "user.name", "Test")
	must("git", "config", "commit.gpgsign", "false")

	for _, skill := range []string{"pdf", "docx"} {
		sd := filepath.Join(dir, "document-skills", skill)
		if err := os.MkdirAll(sd, 0o755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + skill + "\ndescription: test\n---\n"
		if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("git", "add", ".")
	must("git", "commit", "--quiet", "-m", "init")
	return dir
}

func runCLI(t *testing.T, bin, home string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), "SKILLVENDOR_HOME="+home)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func TestEndToEndCLI(t *testing.T) {
	bin := buildBinary(t)
	repo := fixtureSkillsRepo(t)
	home := t.TempDir()
	repoArg := "file://" + repo

	// add
	if out, err := runCLI(t, bin, home, "add", repoArg, "--ref", "main", "--path", "document-skills"); err != nil {
		t.Fatalf("add: %v\n%s", err, out)
	}

	// sync
	if out, err := runCLI(t, bin, home, "sync"); err != nil {
		t.Fatalf("sync: %v\n%s", err, out)
	} else {
		if !strings.Contains(out, "pdf") || !strings.Contains(out, "docx") {
			t.Errorf("sync output missing skills: %s", out)
		}
	}

	// Symlinks exist in both target dirs.
	for _, tgt := range []string{".claude/skills", ".codex/skills"} {
		for _, skill := range []string{"pdf", "docx"} {
			link := filepath.Join(home, tgt, skill)
			info, err := os.Lstat(link)
			if err != nil {
				t.Fatalf("missing symlink %s: %v", link, err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("%s is not a symlink", link)
			}
		}
	}

	// Lockfile records both skills.
	lockBody, err := os.ReadFile(filepath.Join(home, ".config", "skillvendor", "skillvendor.lock"))
	if err != nil {
		t.Fatalf("read lockfile: %v", err)
	}
	if !strings.Contains(string(lockBody), "pdf") || !strings.Contains(string(lockBody), "docx") {
		t.Errorf("lockfile missing entries: %s", lockBody)
	}

	// Re-sync is idempotent and uses locked SHA (we don't have a great way to
	// assert no fetch happened, but it should at least succeed without error).
	if out, err := runCLI(t, bin, home, "sync"); err != nil {
		t.Fatalf("re-sync: %v\n%s", err, out)
	}

	// list shows entry with installed skills.
	out, err := runCLI(t, bin, home, "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pdf") {
		t.Errorf("list missing installed skills: %s", out)
	}

	// Switch to --include to install only one skill.
	if out, err := runCLI(t, bin, home, "add", repoArg, "--ref", "main", "--path", "document-skills", "--include", "pdf"); err != nil {
		t.Fatalf("add (filter): %v\n%s", err, out)
	}
	if out, err := runCLI(t, bin, home, "sync"); err != nil {
		t.Fatalf("sync (filtered): %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude/skills/docx")); !os.IsNotExist(err) {
		t.Errorf("expected docx symlink removed after --include pdf, got %v", err)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude/skills/pdf")); err != nil {
		t.Errorf("pdf should still be installed: %v", err)
	}

	// remove
	if out, err := runCLI(t, bin, home, "remove", repoArg+"#document-skills"); err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude/skills/pdf")); !os.IsNotExist(err) {
		t.Errorf("expected pdf symlink removed after remove, got %v", err)
	}
}

func TestEndToEndConflictRefusal(t *testing.T) {
	bin := buildBinary(t)
	repo := fixtureSkillsRepo(t)
	home := t.TempDir()
	repoArg := "file://" + repo

	// Pre-create a real directory at the target name to simulate a user-owned skill.
	conflict := filepath.Join(home, ".claude", "skills", "pdf")
	if err := os.MkdirAll(conflict, 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := runCLI(t, bin, home, "add", repoArg, "--path", "document-skills", "--include", "pdf"); err != nil {
		t.Fatal(err)
	}
	out, err := runCLI(t, bin, home, "sync")
	if err == nil {
		t.Errorf("expected sync to fail on conflict, got:\n%s", out)
	}
	if _, err := os.Stat(conflict); err != nil {
		t.Errorf("conflicting dir should be preserved: %v", err)
	}
}
