package manifest

import (
	"os"
	"path/filepath"
	"strings"
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

func TestResolvedTargetsDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKILLVENDOR_HOME", home)
	m := &Manifest{}
	got, err := m.ResolvedTargets()
	if err != nil {
		t.Fatalf("ResolvedTargets: %v", err)
	}
	want := []string{
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d targets, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestResolvedTargetsExpandsAndReplaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKILLVENDOR_HOME", home)
	m := &Manifest{Targets: []string{"~/custom/a", "~", "/abs/path"}}
	got, err := m.ResolvedTargets()
	if err != nil {
		t.Fatalf("ResolvedTargets: %v", err)
	}
	want := []string{filepath.Join(home, "custom", "a"), home, "/abs/path"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d: got %q, want %q", i, got[i], want[i])
		}
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

func TestValidateCommand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SKILLVENDOR_HOME", home)
	dir := t.TempDir()
	path := filepath.Join(dir, "skills.yaml")

	cases := []struct {
		name     string
		body     string
		raw      string
		resolved string
		wantErr  bool
	}{
		{"absent", "skills: []\n", "", "", false},
		{"tilde", "validate:\n  command: ~/hooks/review.sh --strict \nskills: []\n", "~/hooks/review.sh --strict", filepath.Join(home, "hooks", "review.sh") + " --strict", false},
		{"absolute", "validate:\n  command: /usr/local/bin/review\nskills: []\n", "/usr/local/bin/review", "/usr/local/bin/review", false},
		{"arguments are not path-cleaned", "validate:\n  command: ~/h.sh --url https://x.example/a/../b//c\nskills: []\n", "~/h.sh --url https://x.example/a/../b//c", home + "/h.sh --url https://x.example/a/../b//c", false},
		{"tilde only in arguments is left to the shell", "validate:\n  command: sh ~/h.sh\nskills: []\n", "sh ~/h.sh", "sh ~/h.sh", false},
		{"empty command", "validate:\n  command: ''\nskills: []\n", "", "", true},
		{"block without command", "validate: {}\nskills: []\n", "", "", true},
		{"relative program resolves in the skill dir", "validate:\n  command: ./review.sh\nskills: []\n", "", "", true},
		{"relative nested program", "validate:\n  command: hooks/review.sh --x\nskills: []\n", "", "", true},
		{"bare name uses PATH", "validate:\n  command: review-skill --strict\nskills: []\n", "review-skill --strict", "review-skill --strict", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			m, err := Load(path)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := m.ValidateCommand(); got != tc.raw {
				t.Errorf("ValidateCommand = %q, want %q", got, tc.raw)
			}
			got, err := m.ResolvedValidateCommand()
			if err != nil || got != tc.resolved {
				t.Errorf("ResolvedValidateCommand = %q, %v; want %q", got, err, tc.resolved)
			}
			// Round-trips through Save without the block appearing when absent.
			if err := m.Save(); err != nil {
				t.Fatal(err)
			}
			saved, _ := os.ReadFile(path)
			if (tc.raw != "") != strings.Contains(string(saved), "validate:") {
				t.Errorf("unexpected saved manifest:\n%s", saved)
			}
		})
	}
}
