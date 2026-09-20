// skillvendor manages remote skills hosted in git repos. It downloads them
// into a local cache and symlinks them into ~/.claude/skills and
// ~/.codex/skills so Claude Code and Codex can discover them.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/cache"
	"github.com/mattjmcnaughton/skillvendor/internal/config"
	"github.com/mattjmcnaughton/skillvendor/internal/manifest"
	"github.com/mattjmcnaughton/skillvendor/internal/review"
	"github.com/mattjmcnaughton/skillvendor/internal/symlink"
	"github.com/mattjmcnaughton/skillvendor/internal/validate"
	"github.com/mattjmcnaughton/skillvendor/internal/version"
)

const usage = `skillvendor — vendor remote skills from git repos.

Usage:
  skillvendor add <repo> [--ref <ref>] [--path <dir>] [--include a,b] [--exclude c,d]
  skillvendor remove <repo>[#<path>]
  skillvendor sync [--update]
  skillvendor list
  skillvendor edit
  skillvendor prompt [--schema]
  skillvendor verdict [--fail-at <risk>]
  skillvendor version
`

// exitError carries a specific process exit code out of a subcommand. An
// empty message means the command already reported the outcome itself.
type exitError struct {
	code int
	msg  string
}

func (e *exitError) Error() string { return e.msg }

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
	case "prompt":
		err = cmdPrompt(args)
	case "verdict":
		err = cmdVerdict(args)
	case "version", "--version", "-v":
		err = cmdVersion(args)
	case "-h", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", sub, usage)
		os.Exit(2)
	}
	var ee *exitError
	if errors.As(err, &ee) {
		if ee.msg != "" {
			fmt.Fprintln(os.Stderr, "error:", ee.msg)
		}
		os.Exit(ee.code)
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
	hookCmd, err := m.ResolvedValidateCommand()
	if err != nil {
		return err
	}
	h := hook{command: hookCmd, hash: validate.HookHash(m.ValidateCommand())}

	manifestKeys := map[string]bool{}
	var rejected []*rejectedError
	for _, e := range m.Skills {
		manifestKeys[e.Key()] = true
		err := syncEntry(e, c, inst, lock, *update, h)
		var rej *rejectedError
		if errors.As(err, &rej) {
			// The entry's symlinks and lock entry are left as they were;
			// keep going so the remaining entries still sync.
			rejected = append(rejected, rej)
			continue
		}
		if err != nil {
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
	if len(rejected) > 0 {
		fmt.Println("rejected by validation hook:")
		for _, r := range rejected {
			fmt.Printf("  %s @ %s — %s\n", r.entry, short(r.sha), r.skill)
		}
		return fmt.Errorf("%d of %d entries rejected by validation hook", len(rejected), len(m.Skills))
	}
	fmt.Println("sync complete")
	return nil
}

// hook is the configured validation hook. An empty command disables it.
type hook struct {
	command string // resolved (`~` expanded) command for /bin/sh -c
	hash    string // SHA-256 of the raw command, recorded in the lock
}

// rejectedError reports that the validation hook rejected one skill of an
// entry, so the whole entry was skipped.
type rejectedError struct {
	entry, sha, skill string
	err               error
}

func (r *rejectedError) Error() string {
	return fmt.Sprintf("%s: %s %v", r.entry, r.skill, r.err)
}

func (r *rejectedError) Unwrap() error { return r.err }

func syncEntry(e manifest.Entry, c *cache.Cache, inst *symlink.Installer, lock *config.Lock, update bool, h hook) error {
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

	skillsDir := skillsDirIn(worktree, e.Path)
	candidates, err := discoverSkills(skillsDir)
	if err != nil {
		return err
	}
	keep := filter(candidates, e.Include, e.Exclude)

	// Prior approvals carry forward (Save prunes those no longer installed),
	// so a hook that is removed and later restored does not re-review
	// unchanged skills.
	validated := make(map[string]config.Validation, len(keep))
	for k, v := range prev.Validated {
		validated[k] = v
	}
	if h.command != "" {
		// Gate every kept skill before touching any symlink: the cache
		// checkout is the quarantine.
		for _, name := range keep {
			tree, err := validate.TreeHash(worktree, e.Path, name)
			if err != nil {
				return err
			}
			if validate.Cached(prev, name, tree, h.hash) {
				fmt.Printf("  %s @ %s — %s: skipped (cached)\n", e.Key(), short(sha), name)
				continue
			}
			env := validate.Env{
				Event:    "install",
				Skill:    name,
				SkillDir: filepath.Join(skillsDir, name),
				Repo:     e.Repo,
				Ref:      e.Ref,
				SHA:      sha,
			}
			if toSet(prev.Installed)[name] {
				env.Event = "update"
				env.PrevSHA = prev.SHA
				if prev.SHA != "" {
					env.PrevSkillDir = filepath.Join(skillsDirIn(c.PathFor(e.Repo, prev.SHA), e.Path), name)
				}
			}
			fmt.Printf("  %s @ %s — %s: validating (%s)\n", e.Key(), short(sha), name, env.Event)
			if err := validate.Run(h.command, env); err != nil {
				if errors.Is(err, validate.ErrRejected) {
					fmt.Printf("  %s @ %s — %s: rejected\n", e.Key(), short(sha), name)
					return &rejectedError{entry: e.Key(), sha: sha, skill: name, err: err}
				}
				return fmt.Errorf("validation hook for %s: %w", name, err)
			}
			fmt.Printf("  %s @ %s — %s: validated\n", e.Key(), short(sha), name)
			validated[name] = config.Validation{Tree: tree, Hook: h.hash}
		}
	}

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
		Validated: validated,
	})
	if len(keep) == 0 {
		fmt.Printf("  %s @ %s — no skills found\n", e.Key(), short(sha))
	} else {
		fmt.Printf("  %s @ %s — installed %s\n", e.Key(), short(sha), strings.Join(keep, ", "))
	}
	return nil
}

// skillsDirIn returns the directory containing skills for an entry path
// within a worktree ("" means the worktree root).
func skillsDirIn(worktree, path string) string {
	if path == "" {
		return worktree
	}
	return filepath.Join(worktree, path)
}

// cmdPrompt prints a self-contained review prompt for the skill described
// by the SKILLVENDOR_* environment, or with --schema only the JSON schema a
// reviewer's response must match.
func cmdPrompt(args []string) error {
	fs := flag.NewFlagSet("prompt", flag.ContinueOnError)
	schema := fs.Bool("schema", false, "print only the JSON response schema")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("prompt takes no arguments")
	}
	if *schema {
		_, err := os.Stdout.Write(review.Schema())
		return err
	}
	p, err := review.Build(review.InputsFromEnv())
	if err != nil {
		return err
	}
	_, err = io.WriteString(os.Stdout, p)
	return err
}

// cmdVerdict reads a reviewer's response from stdin and exits 0 when its
// risk is below --fail-at, 1 when at or above, and 2 when no response with
// a valid risk could be found (or anything else went wrong): a broken
// pipeline must never pass a skill.
func cmdVerdict(args []string) error {
	err := verdict(args)
	var ee *exitError
	if err != nil && !errors.As(err, &ee) {
		return &exitError{code: 2, msg: err.Error()}
	}
	return err
}

func verdict(args []string) error {
	fs := flag.NewFlagSet("verdict", flag.ContinueOnError)
	failAt := fs.String("fail-at", "medium", "lowest risk that fails: none, low, medium, high or critical")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("verdict takes no arguments")
	}
	threshold, ok := review.RiskLevel(*failAt)
	if !ok {
		return fmt.Errorf("invalid --fail-at %q (want none, low, medium, high or critical)", *failAt)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("read stdin: %w", err)
	}
	resp, err := review.Locate(input)
	if err != nil {
		return err
	}
	level, _ := review.RiskLevel(resp.Risk)
	fmt.Fprintf(os.Stderr, "risk: %s\nsummary: %s\n", resp.Risk, strings.TrimSpace(resp.Summary))
	for _, f := range resp.Findings {
		fmt.Fprintf(os.Stderr, "- %s\n", review.FormatFinding(f))
	}
	if level >= threshold {
		fmt.Fprintf(os.Stderr, "verdict: fail (risk %s is at or above %s)\n", resp.Risk, *failAt)
		return &exitError{code: 1}
	}
	fmt.Fprintf(os.Stderr, "verdict: pass (risk %s is below %s)\n", resp.Risk, *failAt)
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

func cmdVersion(args []string) error {
	if len(args) > 0 {
		return errors.New("version takes no arguments")
	}
	fmt.Printf("skillvendor %s\n", version.Version)
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
