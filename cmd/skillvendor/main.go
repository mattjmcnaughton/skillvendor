// skillvendor manages remote skills hosted in git repos. It downloads them
// into a local cache and symlinks them into ~/.claude/skills and
// ~/.codex/skills so Claude Code and Codex can discover them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/cache"
	"github.com/mattjmcnaughton/skillvendor/internal/config"
	"github.com/mattjmcnaughton/skillvendor/internal/manifest"
	"github.com/mattjmcnaughton/skillvendor/internal/symlink"
)

const usage = `skillvendor — vendor remote skills from git repos.

Usage:
  skillvendor add <repo> [--ref <ref>] [--path <dir>] [--include a,b] [--exclude c,d]
  skillvendor remove <repo>[#<path>]
  skillvendor sync [--update]
  skillvendor list
  skillvendor edit
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]

	var err error
	switch sub {
	case "add":
		err = cmdAdd(args)
	case "remove", "rm":
		err = cmdRemove(args)
	case "sync":
		err = cmdSync(args)
	case "list", "ls":
		err = cmdList(args)
	case "edit":
		err = cmdEdit(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", sub, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func loadManifest() (*manifest.Manifest, error) {
	p, err := manifest.DefaultPath()
	if err != nil {
		return nil, err
	}
	return manifest.Load(p)
}

func loadLock() (*config.Lock, error) {
	p, err := config.DefaultPath()
	if err != nil {
		return nil, err
	}
	return config.Load(p)
}

func newInstaller(c *cache.Cache, m *manifest.Manifest) (*symlink.Installer, error) {
	targets, err := m.ResolvedTargets()
	if err != nil {
		return nil, err
	}
	return symlink.New(targets, c.Root()), nil
}

func newCache() (*cache.Cache, error) {
	root, err := cache.DefaultRoot()
	if err != nil {
		return nil, err
	}
	return cache.New(root), nil
}

func cmdAdd(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	ref := fs.String("ref", manifest.DefaultRef, "git ref to track")
	path := fs.String("path", "", "directory within the repo containing skills (default: repo root)")
	includeStr := fs.String("include", "", "comma-separated allowlist of skill subdir names")
	excludeStr := fs.String("exclude", "", "comma-separated denylist of skill subdir names")
	if err := fs.Parse(reorderFlagsFirst(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("add: expected exactly one <repo> argument")
	}
	repo := fs.Arg(0)
	entry := manifest.Entry{
		Repo:    repo,
		Ref:     *ref,
		Path:    *path,
		Include: splitCSV(*includeStr),
		Exclude: splitCSV(*excludeStr),
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}
	if err := m.Upsert(entry); err != nil {
		return err
	}
	if err := m.Save(); err != nil {
		return err
	}

	// Adding/changing an entry invalidates its locked SHA so sync re-resolves
	// the ref. Keep `Installed` so sync can compute the diff and remove skills
	// that are no longer wanted (e.g., after adding an --include filter).
	lock, err := loadLock()
	if err != nil {
		return err
	}
	if locked, ok := lock.Get(repo, *path); ok {
		locked.SHA = ""
		locked.Ref = entry.Ref
		lock.Upsert(locked)
		if err := lock.Save(); err != nil {
			return err
		}
	}
	fmt.Printf("added %s (ref=%s, path=%q). Run `skillvendor sync` to install.\n", entry.Key(), entry.Ref, entry.Path)
	return nil
}

func cmdRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("remove: expected <repo>[#<path>]")
	}
	repo, path := splitRepoPath(args[0])
	m, err := loadManifest()
	if err != nil {
		return err
	}
	if !m.Remove(repo, path) {
		return fmt.Errorf("no manifest entry for %s", args[0])
	}
	if err := m.Save(); err != nil {
		return err
	}

	// Strip lock + symlinks for this entry, if previously installed.
	lock, err := loadLock()
	if err != nil {
		return err
	}
	if locked, ok := lock.Get(repo, path); ok {
		c, err := newCache()
		if err != nil {
			return err
		}
		inst, err := newInstaller(c, m)
		if err != nil {
			return err
		}
		for _, skill := range locked.Installed {
			if err := inst.Remove(skill); err != nil {
				return err
			}
		}
		lock.Remove(repo, path)
		if err := lock.Save(); err != nil {
			return err
		}
	}
	fmt.Printf("removed %s\n", args[0])
	return nil
}

func cmdSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	update := fs.Bool("update", false, "re-resolve refs and rewrite the lockfile")
	if err := fs.Parse(args); err != nil {
		return err
	}

	m, err := loadManifest()
	if err != nil {
		return err
	}
	lock, err := loadLock()
	if err != nil {
		return err
	}
	c, err := newCache()
	if err != nil {
		return err
	}
	inst, err := newInstaller(c, m)
	if err != nil {
		return err
	}

	manifestKeys := map[string]bool{}
	for _, e := range m.Skills {
		manifestKeys[e.Key()] = true
		if err := syncEntry(e, c, inst, lock, *update); err != nil {
			return err
		}
	}

	// Drop lock entries (and their symlinks) for repos no longer in the manifest.
	for i := len(lock.Entries) - 1; i >= 0; i-- {
		e := lock.Entries[i]
		if manifestKeys[e.Key()] {
			continue
		}
		for _, skill := range e.Installed {
			if err := inst.Remove(skill); err != nil {
				return err
			}
		}
		lock.Entries = append(lock.Entries[:i], lock.Entries[i+1:]...)
	}

	if err := lock.Save(); err != nil {
		return err
	}
	fmt.Println("sync complete")
	return nil
}

func syncEntry(e manifest.Entry, c *cache.Cache, inst *symlink.Installer, lock *config.Lock, update bool) error {
	prev, _ := lock.Get(e.Repo, e.Path)

	sha := prev.SHA
	if sha == "" || update {
		resolved, err := c.ResolveRef(e.Repo, e.Ref)
		if err != nil {
			return err
		}
		sha = resolved
	}

	worktree, err := c.Fetch(e.Repo, sha)
	if err != nil {
		return err
	}

	skillsDir := worktree
	if e.Path != "" {
		skillsDir = filepath.Join(worktree, e.Path)
	}
	candidates, err := discoverSkills(skillsDir)
	if err != nil {
		return err
	}
	keep := filter(candidates, e.Include, e.Exclude)

	want := map[string]bool{}
	for _, name := range keep {
		want[name] = true
		src := filepath.Join(skillsDir, name)
		if err := inst.Install(name, src); err != nil {
			return err
		}
	}

	// Skills previously installed from this entry but no longer wanted: remove.
	for _, name := range prev.Installed {
		if !want[name] {
			if err := inst.Remove(name); err != nil {
				return err
			}
		}
	}

	lock.Upsert(config.LockEntry{
		Repo:      e.Repo,
		Path:      e.Path,
		Ref:       e.Ref,
		SHA:       sha,
		Installed: keep,
	})
	if len(keep) == 0 {
		fmt.Printf("  %s @ %s — no skills found\n", e.Key(), short(sha))
	} else {
		fmt.Printf("  %s @ %s — installed %s\n", e.Key(), short(sha), strings.Join(keep, ", "))
	}
	return nil
}

func cmdList(args []string) error {
	if len(args) > 0 {
		return errors.New("list takes no arguments")
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}
	lock, err := loadLock()
	if err != nil {
		return err
	}
	if len(m.Skills) == 0 {
		fmt.Println("(no entries — run `skillvendor add` to register a repo)")
		return nil
	}
	for _, e := range m.Skills {
		locked, ok := lock.Get(e.Repo, e.Path)
		shaStr := "unresolved"
		installed := "—"
		if ok {
			shaStr = short(locked.SHA)
			if len(locked.Installed) > 0 {
				installed = strings.Join(locked.Installed, ", ")
			}
		}
		fmt.Printf("%s\n  ref:%s  sha:%s  installed:[%s]\n", e.Key(), e.Ref, shaStr, installed)
	}
	return nil
}

func cmdEdit(args []string) error {
	if len(args) > 0 {
		return errors.New("edit takes no arguments")
	}
	m, err := loadManifest()
	if err != nil {
		return err
	}
	path := m.Path()
	// Touch the manifest so the editor opens an existing file (avoids "new file" prompts).
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := m.Save(); err != nil {
			return err
		}
	}

	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	cmd := exec.Command(editor, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor exited with error: %w", err)
	}

	// Validate post-edit: reload + check for name collisions across entries.
	reloaded, err := manifest.Load(path)
	if err != nil {
		return fmt.Errorf("manifest is invalid after edit: %w", err)
	}
	if err := checkCollisions(reloaded); err != nil {
		return err
	}
	fmt.Println("manifest saved. Run `skillvendor sync` to apply.")
	return nil
}

// discoverSkills returns the basenames of immediate subdirectories of dir
// that contain a SKILL.md file.
func discoverSkills(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var out []string
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, ent.Name(), "SKILL.md")); err == nil {
			out = append(out, ent.Name())
		}
	}
	return out, nil
}

func filter(candidates, include, exclude []string) []string {
	if len(include) > 0 {
		set := toSet(include)
		var out []string
		for _, c := range candidates {
			if set[c] {
				out = append(out, c)
			}
		}
		return out
	}
	if len(exclude) > 0 {
		set := toSet(exclude)
		var out []string
		for _, c := range candidates {
			if !set[c] {
				out = append(out, c)
			}
		}
		return out
	}
	return candidates
}

// checkCollisions rejects manifests where two entries would install a skill
// with the same name (basename of include allowlist, when set).
func checkCollisions(m *manifest.Manifest) error {
	seen := map[string]string{}
	for _, e := range m.Skills {
		// Only collision-check entries with an explicit allowlist; without
		// `include`, the contents are discovered at sync time.
		for _, name := range e.Include {
			if other, ok := seen[name]; ok {
				return fmt.Errorf("manifest declares skill %q twice (in %s and %s)", name, other, e.Key())
			}
			seen[name] = e.Key()
		}
	}
	return nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitRepoPath(s string) (string, string) {
	if i := strings.Index(s, "#"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

func toSet(xs []string) map[string]bool {
	out := make(map[string]bool, len(xs))
	for _, x := range xs {
		out[x] = true
	}
	return out
}

func short(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// reorderFlagsFirst rewrites args so all flag-shaped tokens (and their values,
// for non-bool flags) come before any positional arguments. Stdlib `flag`
// stops parsing at the first positional, so callers can naturally write
// `skillvendor add <repo> --ref main` even though `flag` expects the reverse.
//
// A token starting with "-" is treated as a flag. If it does NOT contain "="
// and the next token does not start with "-", the next token is consumed as
// its value. This is heuristic but matches every flag this CLI defines.
func reorderFlagsFirst(args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			if !strings.Contains(a, "=") && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				flags = append(flags, args[i+1])
				i++
			}
			continue
		}
		positional = append(positional, a)
	}
	return append(flags, positional...)
}
