package symlink

import (
	"os"
	"path/filepath"
	"testing"
)

func setup(t *testing.T) (i *Installer, cache, targetA, targetB string) {
	t.Helper()
	root := t.TempDir()
	cache = filepath.Join(root, "cache")
	targetA = filepath.Join(root, "claude")
	targetB = filepath.Join(root, "codex")
	for _, d := range []string{cache, targetA, targetB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return New([]string{targetA, targetB}, cache), cache, targetA, targetB
}

func mkSkill(t *testing.T, cache, name string) string {
	t.Helper()
	dir := filepath.Join(cache, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestInstallAndRemove(t *testing.T) {
	i, cache, a, b := setup(t)
	src := mkSkill(t, cache, "pdf")

	if err := i.Install("pdf", src); err != nil {
		t.Fatalf("Install: %v", err)
	}
	for _, target := range []string{a, b} {
		link := filepath.Join(target, "pdf")
		got, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("Readlink %s: %v", link, err)
		}
		if got != src {
			t.Errorf("link target %s, want %s", got, src)
		}
	}

	if !i.IsManaged("pdf") {
		t.Error("expected IsManaged to be true after Install")
	}

	if err := i.Remove("pdf"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	for _, target := range []string{a, b} {
		if _, err := os.Lstat(filepath.Join(target, "pdf")); !os.IsNotExist(err) {
			t.Errorf("expected link removed, got %v", err)
		}
	}
}

func TestInstallIsIdempotent(t *testing.T) {
	i, cache, _, _ := setup(t)
	src := mkSkill(t, cache, "x")
	if err := i.Install("x", src); err != nil {
		t.Fatalf("first install: %v", err)
	}
	if err := i.Install("x", src); err != nil {
		t.Fatalf("second install: %v", err)
	}
}

func TestInstallRefusesExistingDir(t *testing.T) {
	i, cache, a, _ := setup(t)
	src := mkSkill(t, cache, "pdf")
	if err := os.MkdirAll(filepath.Join(a, "pdf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := i.Install("pdf", src); err == nil {
		t.Error("expected error when existing non-symlink at target")
	}
}

func TestInstallRefusesForeignSymlink(t *testing.T) {
	i, cache, a, _ := setup(t)
	src := mkSkill(t, cache, "pdf")
	foreign := t.TempDir()
	if err := os.Symlink(foreign, filepath.Join(a, "pdf")); err != nil {
		t.Fatal(err)
	}
	if err := i.Install("pdf", src); err == nil {
		t.Error("expected error when target is a non-managed symlink")
	}
}

func TestRemoveLeavesForeignAlone(t *testing.T) {
	i, _, a, _ := setup(t)
	foreign := t.TempDir()
	link := filepath.Join(a, "pdf")
	if err := os.Symlink(foreign, link); err != nil {
		t.Fatal(err)
	}
	if err := i.Remove("pdf"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Error("Remove should have left foreign symlink in place")
	}
}

func TestInstallReplacesStaleManagedSymlink(t *testing.T) {
	i, cache, a, _ := setup(t)
	old := mkSkill(t, cache, "old")
	new := mkSkill(t, cache, "new")
	if err := os.Symlink(old, filepath.Join(a, "x")); err != nil {
		t.Fatal(err)
	}
	if err := i.Install("x", new); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, _ := os.Readlink(filepath.Join(a, "x"))
	if got != new {
		t.Errorf("expected stale managed link to be replaced; got %s", got)
	}
}
