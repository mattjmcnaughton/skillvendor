package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	l, err := Load(filepath.Join(dir, "skillvendor.lock"))
	if err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}
	if l.Version != LockVersion {
		t.Errorf("expected default version %d, got %d", LockVersion, l.Version)
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skillvendor.lock")
	l := &Lock{Version: LockVersion, path: path}
	l.Upsert(LockEntry{Repo: "r", Ref: "main", SHA: "abc", Installed: []string{"x"}})
	if err := l.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(body), "# Auto-generated") {
		t.Error("expected auto-generated comment header")
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Entries) != 1 || got.Entries[0].SHA != "abc" {
		t.Errorf("roundtrip lost data: %+v", got.Entries)
	}
}

func TestUpsertReplaces(t *testing.T) {
	l := &Lock{}
	l.Upsert(LockEntry{Repo: "r", SHA: "old"})
	l.Upsert(LockEntry{Repo: "r", SHA: "new"})
	if len(l.Entries) != 1 || l.Entries[0].SHA != "new" {
		t.Errorf("Upsert should replace; got %+v", l.Entries)
	}
}

func TestRemove(t *testing.T) {
	l := &Lock{}
	l.Upsert(LockEntry{Repo: "r"})
	if !l.Remove("r", "") {
		t.Error("Remove returned false for existing entry")
	}
	if l.Remove("r", "") {
		t.Error("Remove returned true for missing entry")
	}
}

func TestUnsupportedVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skillvendor.lock")
	if err := os.WriteFile(path, []byte("version: 99\nentries: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected error on unsupported version")
	}
}
