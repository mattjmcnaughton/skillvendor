// Package validate runs the user's validation hook against a skill in the
// cache before it is symlinked into any target dir, and computes the hashes
// the lockfile uses to skip re-running it.
package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/mattjmcnaughton/skillvendor/internal/config"
)

// HookHash returns the SHA-256 of the trimmed hook command. It keys the
// lock's `validated` records, so changing the validator invalidates every
// prior approval.
func HookHash(command string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(command)))
	return hex.EncodeToString(sum[:])
}

// TreeHash returns the git tree object id of <dir>/<skill> at HEAD of the
// worktree, where dir is the manifest entry's path ("" for the repo root).
func TreeHash(worktree, dir, skill string) (string, error) {
	rel := path.Join(filepath.ToSlash(dir), skill)
	rel = strings.TrimPrefix(rel, "./")
	cmd := exec.Command("git", "-C", worktree, "rev-parse", "HEAD:"+rel)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("tree hash of %s in %s: %w", rel, worktree, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Cached reports whether the lock already records an approval for skill at
// exactly this tree and hook hash.
func Cached(prev config.LockEntry, skill, tree, hook string) bool {
	v, ok := prev.Validated[skill]
	return ok && v.Tree == tree && v.Hook == hook
}

// Env is the hook's contract: the SKILLVENDOR_* variables it receives.
type Env struct {
	Event        string // "install" or "update"
	Skill        string
	SkillDir     string // absolute path of the new skill dir in the cache
	PrevSkillDir string // previous version's dir; empty on install
	Repo         string
	Ref          string
	SHA          string
	PrevSHA      string // previously locked commit; empty on install
}

// Environ returns the variables to append to the hook's environment.
func (e Env) Environ() []string {
	return []string{
		"SKILLVENDOR_EVENT=" + e.Event,
		"SKILLVENDOR_SKILL=" + e.Skill,
		"SKILLVENDOR_SKILL_DIR=" + e.SkillDir,
		"SKILLVENDOR_PREV_SKILL_DIR=" + e.PrevSkillDir,
		"SKILLVENDOR_REPO=" + e.Repo,
		"SKILLVENDOR_REF=" + e.Ref,
		"SKILLVENDOR_SHA=" + e.SHA,
		"SKILLVENDOR_PREV_SHA=" + e.PrevSHA,
	}
}

// ErrRejected wraps a hook's nonzero exit so callers can tell a rejection
// from a hook that could not be run at all.
var ErrRejected = errors.New("rejected by validation hook")

// Run executes command via `/bin/sh -c` with the skill dir as the working
// directory and stdin/stdout/stderr inherited, so hooks may be interactive.
// There is no timeout; a hook that needs one wraps itself in timeout(1). A
// nonzero exit returns an error wrapping ErrRejected; any other failure is
// returned as-is.
func Run(command string, env Env) error {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = env.SkillDir
	cmd.Env = append(os.Environ(), env.Environ()...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return fmt.Errorf("%w (exit status %d)", ErrRejected, exit.ExitCode())
	}
	return err
}
