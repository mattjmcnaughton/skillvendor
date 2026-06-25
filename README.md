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

### `list`

Shows every manifest entry alongside the ref it tracks and the SHA it's locked to. Drift between the two indicates that `sync --update` would move the install.

### `edit`

Opens `~/.config/skillvendor/skills.yaml` in `$VISUAL`, then `$EDITOR`, then `vi`. After the editor exits, the manifest is reloaded and validated; an invalid edit prints an error (and the broken file remains on disk for you to fix).

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
