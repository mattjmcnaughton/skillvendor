package review

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustBuild(t *testing.T, in Inputs) string {
	t.Helper()
	out, err := build(in, "cafef00d")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	return out
}

func TestBuildInstallInventoryAndContents(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\nDo things"))
	writeFile(t, filepath.Join(dir, "scripts", "run.sh"), []byte("#!/bin/sh\necho hi\n"))

	out := mustBuild(t, Inputs{Skill: "x", SkillDir: dir, Repo: "github.com/a/b", Ref: "main", SHA: "abc"})

	for _, want := range []string{
		"# Skill review: x (install)",
		"Repo: github.com/a/b",
		`added           25  "SKILL.md"`,
		`added           18  "scripts/run.sh"`,
		"<<<SKILLVENDOR:UNTRUSTED:BEGIN nonce=cafef00d>>>",
		"<<<SKILLVENDOR:FILE:BEGIN path=\"SKILL.md\" nonce=cafef00d>>>\n---\nname: x\n---\nDo things\n<<<SKILLVENDOR:FILE:END path=\"SKILL.md\" nonce=cafef00d>>>",
		"<<<SKILLVENDOR:FILE:BEGIN path=\"scripts/run.sh\" nonce=cafef00d>>>\n#!/bin/sh\necho hi\n<<<SKILLVENDOR:FILE:END",
		"<<<SKILLVENDOR:UNTRUSTED:END nonce=cafef00d>>>",
		`"$id": "skillvendor/review/v1"`,
		"reviewer-manipulation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, out)
		}
	}
	// Section order: framing, rubric, inventory, contents, output.
	idx := func(s string) int { return strings.Index(out, s) }
	if !(idx("Rules:") < idx("## Rubric") && idx("## Rubric") < idx("## Inventory") &&
		idx("## Inventory") < idx("## Contents") && idx("## Contents") < idx("## Output")) {
		t.Errorf("sections out of order:\n%s", out)
	}
	if strings.Contains(out, "Previous commit") {
		t.Error("install prompt should not mention a previous commit")
	}
}

func TestBuildUpdateStatuses(t *testing.T) {
	prev := t.TempDir()
	cur := t.TempDir()
	writeFile(t, filepath.Join(prev, "SKILL.md"), []byte("same\n"))
	writeFile(t, filepath.Join(prev, "old.txt"), []byte("gone\n"))
	writeFile(t, filepath.Join(prev, "edit.txt"), []byte("v1\n"))
	writeFile(t, filepath.Join(cur, "SKILL.md"), []byte("same\n"))
	writeFile(t, filepath.Join(cur, "edit.txt"), []byte("v2\n"))
	writeFile(t, filepath.Join(cur, "new.txt"), []byte("hello\n"))

	out := mustBuild(t, Inputs{Event: "update", SkillDir: cur, PrevSkillDir: prev, SHA: "new", PrevSHA: "old"})

	for _, want := range []string{
		`unchanged        5  "SKILL.md"`,
		`changed          3  "edit.txt"`,
		`added            6  "new.txt"`,
		`removed          5  "old.txt"`,
		"Previous commit: old",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, "gone") {
		t.Error("removed file content must not be inlined")
	}
	if !strings.Contains(out, "\nv2\n") {
		t.Error("changed file's new content should be inlined")
	}
}

func TestBuildNeverFollowsSymlinks(t *testing.T) {
	outside := t.TempDir()
	secret := []byte("-----BEGIN OPENSSH PRIVATE KEY-----\nhunter2\n")
	writeFile(t, filepath.Join(outside, "id_ed25519"), secret)
	writeFile(t, filepath.Join(outside, "tree", "leak.txt"), []byte("leaked dir content\n"))

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("hi\n"))
	writeFile(t, filepath.Join(dir, "real.txt"), []byte("real\n"))
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(outside, "id_ed25519"), filepath.Join(dir, "key")))
	must(os.Symlink("../../"+filepath.Base(outside)+"/id_ed25519", filepath.Join(dir, "relkey")))
	must(os.Symlink(filepath.Join(outside, "tree"), filepath.Join(dir, "linkdir")))
	must(os.Symlink("real.txt", filepath.Join(dir, "inside")))
	must(os.Symlink("./real.txt", filepath.Join(dir, "sub", "..", "dotinside")))

	// Also make the previous dir a symlink farm to prove scan(prev) does
	// not follow either.
	prev := t.TempDir()
	must(os.Symlink(filepath.Join(outside, "id_ed25519"), filepath.Join(prev, "SKILL.md")))

	out := mustBuild(t, Inputs{Event: "update", SkillDir: dir, PrevSkillDir: prev})

	if bytes.Contains([]byte(out), secret) || strings.Contains(out, "hunter2") {
		t.Fatalf("private key content was inlined:\n%s", out)
	}
	if strings.Contains(out, "leaked dir content") {
		t.Fatalf("symlinked directory was descended:\n%s", out)
	}
	for _, want := range []string{
		`"key"  -> "` + filepath.Join(outside, "id_ed25519") + `"  (unreviewable: symlink target outside skill dir)  (symlink, not followed)`,
		`"relkey"  -> "../../` + filepath.Base(outside) + `/id_ed25519"  (unreviewable: symlink target outside skill dir)`,
		`"linkdir"  -> "` + filepath.Join(outside, "tree") + `"  (unreviewable: symlink target outside skill dir)`,
		`"inside"  -> "real.txt"  (symlink, not followed)`,
		`"dotinside"  -> "./real.txt"  (symlink, not followed)`,
		// prev SKILL.md was a symlink, current is a file: changed.
		`changed          3  "SKILL.md"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, out)
		}
	}
	if strings.Contains(out, `"inside"  -> "real.txt"  (unreviewable`) {
		t.Error("in-tree symlink should not be flagged unreviewable")
	}
	if strings.Contains(out, `path="key"`) || strings.Contains(out, `path="inside"`) {
		t.Error("symlinks must not be inlined even when they point inside the skill")
	}
}

func TestBuildRootMayBeASymlink(t *testing.T) {
	real := t.TempDir()
	writeFile(t, filepath.Join(real, "SKILL.md"), []byte("via link\n"))
	link := filepath.Join(t.TempDir(), "skill")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	out := mustBuild(t, Inputs{SkillDir: link})
	if !strings.Contains(out, "via link") {
		t.Errorf("installed symlink root was not walked:\n%s", out)
	}
	if !strings.Contains(out, "# Skill review: skill (install)") {
		t.Errorf("skill name should default to the given dir's basename:\n%s", out)
	}
}

func TestBuildFlagsBinaryAndLargeFiles(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("ok\n"))
	writeFile(t, filepath.Join(dir, "blob.bin"), []byte("PK\x00\x01binary-marker-zz"))
	big := bytes.Repeat([]byte("big-marker-yy\n"), MaxFileBytes/14+1)
	writeFile(t, filepath.Join(dir, "big.txt"), big)
	bigbin := append([]byte("\x00"), bytes.Repeat([]byte("x"), MaxFileBytes)...)
	writeFile(t, filepath.Join(dir, "big.bin"), bigbin)

	out := mustBuild(t, Inputs{SkillDir: dir})
	for _, want := range []string{
		`"blob.bin"  (unreviewable: binary)`,
		`"big.txt"  (unreviewable: larger than 64 KiB)`,
		`"big.bin"  (unreviewable: binary)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("prompt missing %q\n---\n%s", want, out)
		}
	}
	for _, leak := range []string{"binary-marker-zz", "big-marker-yy"} {
		if strings.Contains(out, leak) {
			t.Errorf("%s should not be inlined", leak)
		}
	}
}

func TestBuildTotalBudgetDropsLargestFirst(t *testing.T) {
	dir := t.TempDir()
	// Nine 60 KiB files = 540 KiB > 512 KiB budget; the largest goes first.
	for i := 0; i < 8; i++ {
		writeFile(t, filepath.Join(dir, "f"+string(rune('a'+i))+".txt"), bytes.Repeat([]byte("s"), 60<<10))
	}
	writeFile(t, filepath.Join(dir, "largest.txt"), bytes.Repeat([]byte("L"), 61<<10))
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("tiny\n"))

	out := mustBuild(t, Inputs{SkillDir: dir})
	if !strings.Contains(out, `"largest.txt"  (unreviewable: dropped to fit the 512 KiB prompt budget)`) {
		t.Errorf("largest file should be dropped:\n%s", out[:2000])
	}
	if strings.Contains(out, `path="largest.txt"`) {
		t.Error("dropped file must not be inlined")
	}
	if !strings.Contains(out, `path="fa.txt"`) || !strings.Contains(out, "tiny\n") {
		t.Error("files within budget should still be inlined")
	}
}

func TestBuildErrors(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("x"))
	cases := map[string]Inputs{
		"missing dir":            {},
		"nonexistent dir":        {SkillDir: filepath.Join(dir, "nope")},
		"bad event":              {SkillDir: dir, Event: "remove"},
		"missing prev on update": {SkillDir: dir, Event: "update", PrevSkillDir: filepath.Join(dir, "nope")},
	}
	for name, in := range cases {
		if _, err := build(in, "n"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestBuildUsesRandomNonce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "SKILL.md"), []byte("x"))
	re := regexp.MustCompile(`UNTRUSTED:BEGIN nonce=([0-9a-f]{32})>>>`)
	a, err := Build(Inputs{SkillDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Build(Inputs{SkillDir: dir})
	na, nb := re.FindStringSubmatch(a), re.FindStringSubmatch(b)
	if na == nil || nb == nil || na[1] == nb[1] {
		t.Errorf("expected distinct 32-hex nonces, got %v / %v", na, nb)
	}
	if strings.Count(a, na[1]) < 4 {
		t.Errorf("nonce should appear on every delimiter, found %d", strings.Count(a, na[1]))
	}
}

func TestInputsFromEnv(t *testing.T) {
	t.Setenv("SKILLVENDOR_EVENT", "update")
	t.Setenv("SKILLVENDOR_SKILL", "pdf")
	t.Setenv("SKILLVENDOR_SKILL_DIR", "/a")
	t.Setenv("SKILLVENDOR_PREV_SKILL_DIR", "/b")
	t.Setenv("SKILLVENDOR_REPO", "r")
	t.Setenv("SKILLVENDOR_REF", "main")
	t.Setenv("SKILLVENDOR_SHA", "1")
	t.Setenv("SKILLVENDOR_PREV_SHA", "0")
	got := InputsFromEnv()
	want := Inputs{Event: "update", Skill: "pdf", SkillDir: "/a", PrevSkillDir: "/b", Repo: "r", Ref: "main", SHA: "1", PrevSHA: "0"}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
