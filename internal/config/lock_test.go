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

func TestValidatedRoundtripAndPrune(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skillvendor.lock")
	l := &Lock{Version: LockVersion, path: path}
	l.Upsert(LockEntry{
		Repo: "r", Ref: "main", SHA: "abc", Installed: []string{"pdf"},
		Validated: map[string]Validation{
			"pdf":  {Tree: "t1", Hook: "h1"},
			"docx": {Tree: "t2", Hook: "h1"}, // no longer installed: dropped on save
		},
	})
	l.Upsert(LockEntry{Repo: "s", Ref: "main", SHA: "def", Installed: []string{"x"},
		Validated: map[string]Validation{"y": {Tree: "t", Hook: "h"}}})
	if err := l.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	body, _ := os.ReadFile(path)
	if strings.Contains(string(body), "docx") {
		t.Errorf("validated record for uninstalled skill should be pruned:\n%s", body)
	}
	if !strings.Contains(string(body), "validated:") || !strings.Contains(string(body), "tree: t1") {
		t.Errorf("validated map not written:\n%s", body)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if v := got.Entries[0].Validated["pdf"]; v != (Validation{Tree: "t1", Hook: "h1"}) {
		t.Errorf("roundtrip lost validated record: %+v", got.Entries[0])
	}
	if got.Entries[1].Validated != nil {
		t.Errorf("fully pruned map should be omitted, got %+v", got.Entries[1].Validated)
	}
}

func TestLoadIgnoresUnknownFieldsAndOldLocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "skillvendor.lock")
	body := "version: 1\nentries:\n  - repo: r\n    ref: main\n    sha: abc\n    installed: [pdf]\n    future: 1\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Entries[0].Validated != nil {
		t.Errorf("expected no validated map on a pre-validation lock, got %+v", got.Entries[0].Validated)
	}
}
