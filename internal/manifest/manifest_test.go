package manifest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	m, err := Load(filepath.Join(dir, "skills.yaml"))
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if len(m.Skills) != 0 {
		t.Errorf("expected empty manifest, got %d entries", len(m.Skills))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills.yaml")
	m := &Manifest{path: path}
	if err := m.Upsert(Entry{Repo: "github.com/foo/bar", Path: "skills", Include: []string{"a", "b"}}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := m.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Skills) != 1 || got.Skills[0].Ref != DefaultRef {
		t.Errorf("expected one entry with default ref, got %+v", got.Skills)
	}
}

func TestUpsertReplaces(t *testing.T) {
	m := &Manifest{path: "/dev/null"}
	_ = m.Upsert(Entry{Repo: "r", Path: "p", Ref: "main"})
	_ = m.Upsert(Entry{Repo: "r", Path: "p", Ref: "v2"})
	if len(m.Skills) != 1 || m.Skills[0].Ref != "v2" {
		t.Errorf("Upsert should replace; got %+v", m.Skills)
	}
}

func TestRemove(t *testing.T) {
	m := &Manifest{path: "/dev/null"}
	_ = m.Upsert(Entry{Repo: "r", Path: "p"})
	if !m.Remove("r", "p") {
		t.Error("Remove returned false for existing entry")
	}
	if m.Remove("r", "p") {
		t.Error("Remove returned true for missing entry")
	}
}

func TestValidateMutuallyExclusiveFilters(t *testing.T) {
	e := Entry{Repo: "r", Include: []string{"a"}, Exclude: []string{"b"}}
	if err := e.Validate(); err == nil {
		t.Error("expected error when include and exclude both set")
	}
}

func TestEntryKey(t *testing.T) {
	if k := (Entry{Repo: "r"}).Key(); k != "r" {
		t.Errorf("got %q", k)
	}
	if k := (Entry{Repo: "r", Path: "p"}).Key(); k != "r#p" {
		t.Errorf("got %q", k)
	}
}

func TestRejectsInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skills.yaml")
	if err := os.WriteFile(path, []byte("skills: not-a-list"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error on malformed yaml")
	}
}
