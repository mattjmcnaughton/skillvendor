# skillvendor

Vendor remote skills from git repos into `~/.claude/skills` and `~/.codex/skills`.

`skillvendor` clones git repositories into a local cache and symlinks the skills they contain into the directories Claude Code and Codex read. A lockfile pins each manifest entry to a resolved commit SHA so installs are reproducible across machines.

Hand-authored skills (your own) are not in scope — symlink your local skills repo into those directories yourself. `skillvendor` only manages the remote ones.

## Install

```
go install github.com/mattjmcnaughton/skillvendor/cmd/skillvendor@latest
```

Or, from a checkout:
```
just install
```

## Commands

```
skillvendor add <repo> [--ref <ref>] [--path <dir>] [--include a,b] [--exclude c,d]
skillvendor remove <repo>[#<path>]
skillvendor sync [--update]
skillvendor list
skillvendor edit
skillvendor prompt [--schema]
skillvendor verdict [--fail-at <risk>]
```

### `add`

Register a remote skills source.

```
skillvendor add github.com/anthropics/skills --ref main --path document-skills
skillvendor add github.com/foo/bar --ref v1.2.0 --include example,helper
```

- `--ref` defaults to `main`. It accepts a branch, a tag, a fully-qualified ref
  (e.g. `refs/pull/1/head`), or a commit SHA. A full-length SHA (40 hex chars,
  or 64 for SHA-256) is recognized as a pin automatically; prefix any SHA with
  `sha:` (e.g. `sha:4f1a2b3`) to force pinning, including abbreviated SHAs.
  A pinned SHA resolves to itself, so `sync --update` never moves it.
- `--path` points to a directory **containing skills**. Each immediate subdir with a `SKILL.md` becomes one installed skill. If omitted, the repo root is treated as that directory.
- `--include` and `--exclude` filter by subdir basename. They are mutually exclusive.

### `remove`

```
skillvendor remove github.com/foo/bar
skillvendor remove github.com/anthropics/skills#document-skills
```

Drops the entry from the manifest, removes its symlinks, and removes its lock entry. The cache directory is retained.

### `sync`

```
skillvendor sync             # use locked SHAs; resolve refs only for new entries
skillvendor sync --update    # re-resolve every ref, rewrite the lockfile
```

- Idempotent. Running `sync` with no manifest changes does no network work after the first run.
- Removes symlinks for skills no longer resolved by the manifest (e.g., after tightening an `--include` filter).
- Errors loudly on naming conflicts in the target dirs and never overwrites user-owned content.
- When a validation hook is configured, every skill passes through it before it is symlinked (see [Validation](#validation)).

### `list`

Shows every manifest entry alongside the ref it tracks and the SHA it's locked to. Drift between the two indicates that `sync --update` would move the install.

### `edit`

Opens `~/.config/skillvendor/skills.yaml` in `$VISUAL`, then `$EDITOR`, then `vi`. After the editor exits, the manifest is reloaded and validated; an invalid edit prints an error (and the broken file remains on disk for you to fix).

## Validation

An optional hook gates skills on install and update: nothing is symlinked into a target dir (and so becomes visible to Claude Code or Codex) until the hook has passed it. The cache checkout is the quarantine. `skillvendor` takes no dependency on any AI CLI; it can emit a review prompt and parse the response, and you pick what runs in between.

Enable it with a `validate` block in the manifest:

```yaml
validate:
  command: ~/.config/skillvendor/hooks/review.sh
```

`command` is a string run via `/bin/sh -c`; a leading `~` expands like `targets`. There are no other keys and no flags. To turn validation off, remove the block; to skip one repo, exit 0 early in the hook (see the example below).

### Hook contract

The hook runs once per skill, after the manifest's `include`/`exclude` filters and before any symlink is created. Its working directory is the skill's cached directory. Stdin, stdout and stderr are inherited, so a hook may prompt you interactively. There is no timeout: a hook that needs one wraps itself in `timeout(1)`.

| Variable | Value |
|---|---|
| `SKILLVENDOR_EVENT` | `install` or `update` |
| `SKILLVENDOR_SKILL` | skill name (dir basename) |
| `SKILLVENDOR_SKILL_DIR` | absolute path of the new skill dir in the cache |
| `SKILLVENDOR_PREV_SKILL_DIR` | previous version's dir in the cache; empty on `install`, or if that worktree is no longer cached |
| `SKILLVENDOR_REPO`, `SKILLVENDOR_REF`, `SKILLVENDOR_SHA` | entry identity and resolved commit |
| `SKILLVENDOR_PREV_SHA` | previously locked commit; empty on `install` |

Exit 0 passes. Any other exit code fails, and failure applies to the whole manifest entry: none of its skills are (re)linked and its lock entry is left as it was, so a failed update keeps the last good version installed. `sync` continues with the remaining entries, prints a summary of rejected entries at the end, and exits 1.

The event is `update` when the lockfile already lists the skill as installed from that entry; otherwise `install`.

### Caching

The hook runs only when a skill's content or the hook itself changes. The lockfile records, per skill, the git tree hash of the skill directory and the SHA-256 of the `validate.command` string; while both match, `sync` prints `skipped (cached)` instead of running the hook. Editing the command (even trivially) invalidates every prior approval, so a new hook is exercised on the next `sync`.

### `prompt` and `verdict`

Two helpers make an LLM-backed review a one-line hook.

`skillvendor prompt` prints a self-contained review prompt to stdout, built from the `SKILLVENDOR_*` environment: framing that tells the reviewer everything below the delimiter is untrusted data and must not be executed, a rubric (credential exfiltration, unexpected network access, privilege escalation, destructive operations, obfuscation, instructions to disable safeguards, unreadable executables), an inventory of every path with size and status (`added`, `changed`, `unchanged`, `removed` relative to the previous version), the contents of each text file wrapped in delimiters carrying a per-run random nonce, and an instruction to answer with a single JSON object. Binary files, text files over 64 KiB, and symlinks are listed but never inlined; symlinks are never followed, and one that points outside the skill dir is flagged `unreviewable`. Inlined content is capped at 512 KiB, largest files dropped first. For ad-hoc use outside a hook, set `SKILLVENDOR_SKILL_DIR` by hand, e.g. to an installed symlink. `skillvendor prompt --schema` prints only the JSON schema (`skillvendor/review/v1`) the response must match:

```json
{ "risk": "none|low|medium|high|critical", "summary": "...", "findings": [ { "severity": "...", "category": "...", "file": "...", "line": 1, "description": "..." } ] }
```

The model reports risk; it does not decide pass or fail. That is policy, and it lives in `verdict`.

`skillvendor verdict [--fail-at <risk>]` reads the model's response from stdin, prints the summary and findings to stderr, and exits:

- `0` when `risk` is below the threshold (`--fail-at` defaults to `medium`),
- `1` when it is at or above,
- `2` when no JSON object with a valid `risk` was found. This fails closed and distinguishes a broken pipeline from a risky skill.

It accepts Claude Code's `--output-format json` envelope (`structured_output` first, then the `result` text), a bare response object, or the last well-formed JSON object in free text, fenced or not.

### Example hook

```sh
#!/bin/sh
set -eu
# Skip repos you own.
case "$SKILLVENDOR_REPO" in github.com/me/*) exit 0 ;; esac
skillvendor prompt \
  | timeout 5m claude --bare -p --permission-mode dontAsk --max-turns 1 \
      --output-format json --json-schema "$(skillvendor prompt --schema)" \
  | skillvendor verdict --fail-at medium
```

`--bare` stops Claude Code loading your own skills, including the one under review. `dontAsk` with no allowlist denies every tool call, so the model can only read what the prompt inlined. An LLM review is a filter, not a proof: it raises the bar against careless or opportunistic skills, and it can be wrong in both directions.

## File layout

```
~/.config/skillvendor/
  skills.yaml          # hand-edited manifest
  skillvendor.lock     # auto-generated SHA pins; commit alongside dotfiles for reproducibility

~/.cache/skillvendor/
  <host>/<owner>/<repo>@<sha>/   # checked-out git worktrees

~/.claude/skills/<skill>  -> ~/.cache/skillvendor/.../<skill>
~/.codex/skills/<skill>   -> ~/.cache/skillvendor/.../<skill>
```

### Manifest format (`skills.yaml`)

```yaml
# Optional. Directories that managed skills are symlinked into.
# When omitted, defaults to ~/.claude/skills and ~/.codex/skills.
# When set, it REPLACES the defaults — include them explicitly if you still want them.
# `~` and `~/...` are expanded against $HOME (or $SKILLVENDOR_HOME if set).
targets:
  - ~/.claude/skills
  - ~/.codex/skills
  - ~/projects/team-skills

skills:
  - repo: github.com/anthropics/skills
    ref: main
    path: document-skills          # optional; defaults to repo root
    include: [pdf, docx]           # optional allowlist
    # exclude: [pptx]              # optional denylist; mutually exclusive with include
  - repo: github.com/foo/bar
    ref: v1.2.0
    exclude: [experimental]
  - repo: github.com/baz/qux
    ref: 4f1a2b3c4d5e6f7890abcdef1234567890abcdef  # pin to a commit
  - repo: github.com/baz/qux
    ref: sha:4f1a2b3                               # force-pin an abbreviated SHA

# Optional. Run this command (via /bin/sh -c) on every skill before it is
# installed or updated; a nonzero exit blocks the entry. See "Validation".
validate:
  command: ~/.config/skillvendor/hooks/review.sh
```

A `ref` that is a full-length commit SHA (or any SHA prefixed with `sha:`) is
treated as a pin: it resolves to itself with no network lookup, so `sync
--update` leaves it in place.

### Lockfile format (`skillvendor.lock`)

Auto-generated; do not edit by hand.

```yaml
version: 1
entries:
  - repo: github.com/anthropics/skills
    path: document-skills
    ref: main
    sha: 4f1a2b3c4d5e6f7890abcdef1234567890abcdef
    installed: [pdf, docx]
    # Present only when a validation hook approved the skill: the skill's
    # git tree hash and the SHA-256 of the hook command at that time.
    validated:
      pdf: {tree: 9c1e2d..., hook: 3b7a...}
      docx: {tree: 51ff0a..., hook: 3b7a...}
```

## Sandboxing

Set `SKILLVENDOR_HOME` to redirect every path (manifest, lockfile, cache, target dirs) under a custom root. Useful for tests and for running parallel installations.

```
SKILLVENDOR_HOME=/tmp/sandbox skillvendor add ...
```

## Conflict behavior

`skillvendor` refuses to overwrite anything it didn't install:

- An existing **non-symlink** at a target path → error, no changes.
- An existing **symlink pointing outside the cache** → error, no changes.
- A stale symlink pointing **into the cache** → replaced silently (this is how `sync` reflects ref or filter changes).
