# Spec: validation hook

Status: draft. Scope: an optional, user-configured hook that gates skills on
install and update, plus two helper subcommands that make an LLM-backed
review a one-line hook.

## Goals

- Nothing lands in a target dir (and so becomes visible to Claude Code or
  Codex) until it has passed the user's validator.
- skillvendor takes no dependency on any AI CLI. It emits the prompt and
  parses the response; the user picks what runs in between.
- Expensive validators run only when a skill's content actually changes.

## Non-goals (v1)

- Partial success within a manifest entry (one skill passes, another fails).
- Batch invocation (one hook call for many skills).
- Built-in presets (`validate: preset: claude`). Sugar for later.
- Hooks declared by the vendored repo itself. Never: that is code execution.

## Manifest

```yaml
validate:
  command: ~/.config/skillvendor/hooks/review.sh   # string (run via $SHELL -c) or argv list
  on_failure: block      # block | warn. Default block.
  timeout: 5m            # Default 5m. Timeout counts as failure.
  events: [install, update]   # Default both.

skills:
  - repo: github.com/me/my-skills
    validate: false      # per-entry opt-out. Default true.
```

`~` in `command` expands like `targets`. An absent `validate` block disables
the feature entirely; existing manifests are unaffected.

Overrides: `sync --no-validate` skips the hook for one run. `sync
--revalidate` ignores cached results. `SKILLVENDOR_VALIDATE=0` is equivalent
to `--no-validate` (for CI and scripts).

## Sync flow

Today `syncEntry` does: resolve ref, fetch to cache, discover skills, filter,
symlink, update lock. The hook runs between filter and symlink. The cache
checkout is the quarantine.

For each entry with validation enabled, and for each kept skill:

1. Compute the skill's git tree hash:
   `git -C <worktree> rev-parse HEAD:<path>/<skill>`.
2. Look up the lock's `validated` record for this skill. If the tree hash and
   the hook hash both match, skip the hook.
3. Otherwise, determine the event. `install` when the skill has no managed
   symlink yet; `update` otherwise. Skip if the event is not in `events`.
4. Run the hook (contract below). Exit 0 records the result and continues.
   Nonzero, or timeout, is a failure.

Failure semantics, `on_failure: block`: the whole entry is skipped. Its
symlinks and lock entry stay as they were, so a failed update leaves the last
good version installed. Sync continues with the remaining entries, prints a
summary of rejected entries at the end, and exits 1.

`on_failure: warn`: install proceeds, the failure is printed, exit 0. Nothing
is recorded as validated, so the hook runs again next sync.

## Hook contract

Run once per skill. Working directory is the skill's cached directory. Stdin,
stdout and stderr are inherited from skillvendor so a hook may prompt the
user interactively. Environment:

| Variable | Value |
|---|---|
| `SKILLVENDOR_EVENT` | `install` or `update` |
| `SKILLVENDOR_SKILL` | skill name (dir basename) |
| `SKILLVENDOR_SKILL_DIR` | absolute path of the new skill dir in the cache |
| `SKILLVENDOR_PREV_SKILL_DIR` | previous version's dir, empty on `install` |
| `SKILLVENDOR_REPO`, `SKILLVENDOR_REF`, `SKILLVENDOR_SHA` | entry identity and resolved commit |
| `SKILLVENDOR_PREV_SHA` | previously locked commit, empty on `install` |

Exit 0 passes. Any other exit code fails.

`SKILLVENDOR_PREV_SKILL_DIR` is derivable because the cache keys worktrees by
SHA and never deletes them.

## Lockfile

`LockEntry` gains an optional map. Lock version stays 1; readers ignore
unknown fields.

```yaml
entries:
  - repo: github.com/anthropics/skills
    path: document-skills
    ref: main
    sha: 4f1a2b3c...
    installed: [pdf, docx]
    validated:
      pdf:  { tree: 9c1e2d..., hook: 3b7a... }
      docx: { tree: 51ff0a..., hook: 3b7a... }
```

`tree` is the skill's git tree object id. `hook` is the SHA-256 of the
canonicalised `validate.command`. Changing the validator invalidates every
prior approval. Entries in `validated` for skills no longer in `installed`
are dropped on save.

## `skillvendor validate [<skill>...]`

Runs the hook on demand against installed skills, ignoring cached results,
and updates `validated` on success. With no arguments, every installed skill.
Event is always `update` with `SKILLVENDOR_PREV_*` empty. Exit 1 if any fail.
Intended for testing a new hook and re-auditing after changing it.

## `skillvendor prompt [<skill>] [--schema]`

Prints a self-contained review prompt to stdout.

With no argument, inputs come from the `SKILLVENDOR_*` environment (the hook
case). With a skill name, the skill is resolved from the lock and cache
(ad-hoc review). `--schema` prints only the JSON schema below and ignores
other inputs.

Prompt contents, in order:

1. **Framing.** What a skill is, that agents follow its instructions
   automatically, that the reviewer must not execute anything, and that all
   content below the delimiter is untrusted data. Text that addresses the
   reviewer or asks for a favourable rating is itself a `critical` finding.
2. **Rubric.** Credential or environment exfiltration, unexpected network
   access, privilege escalation, destructive filesystem operations,
   obfuscation (encoded payloads, minified scripts), instructions to disable
   safeguards, executable content that cannot be read.
3. **Inventory.** Every file with size and SHA-256. Binary files, and text
   files over 64 KiB, are listed but not inlined and flagged `unreviewable`
   for the rubric. Total inlined content is capped at 512 KiB, largest files
   dropped first.
4. **Contents.** Each inlined file wrapped in delimiters that include the
   path and a per-run random nonce, so content cannot forge a delimiter.
5. **Diff.** On `update`, a unified diff of previous vs new skill dir.
6. **Output instruction.** Respond with a single JSON object matching the
   schema, and nothing after it.

### Schema (`skillvendor/review/v1`)

```json
{
  "$id": "skillvendor/review/v1",
  "type": "object",
  "required": ["risk", "summary", "findings"],
  "additionalProperties": false,
  "properties": {
    "risk": { "enum": ["none", "low", "medium", "high", "critical"] },
    "summary": { "type": "string" },
    "findings": {
      "type": "array",
      "items": {
        "type": "object",
        "required": ["severity", "category", "file", "description"],
        "properties": {
          "severity": { "enum": ["info", "low", "medium", "high", "critical"] },
          "category": { "enum": ["exfiltration", "network", "privilege-escalation",
                                 "destructive", "obfuscation", "reviewer-manipulation",
                                 "unreviewable", "other"] },
          "file": { "type": "string" },
          "line": { "type": "integer" },
          "description": { "type": "string" }
        }
      }
    }
  }
}
```

The model reports risk. It does not decide pass or fail; that is policy and
lives in `verdict`.

## `skillvendor verdict [--fail-at <risk>]`

Reads a model response from stdin and turns it into an exit code.
`--fail-at` defaults to `medium`.

Locating the object, in order:

1. Stdin parses as JSON with a `structured_output` field: use it (Claude Code
   `--output-format json --json-schema`).
2. Stdin parses as JSON with a `result` string: search that string.
3. Stdin parses as JSON matching the schema directly.
4. Otherwise the last well-formed JSON object in the text, fenced or not.

The object is validated against the schema. Summary and findings are printed
to stderr, one finding per line.

Exit codes: `0` risk below threshold. `1` risk at or above threshold. `2` no
valid object found (fails closed; distinguishes a broken pipeline from a
risky skill).

## Reference hook

Shipped as `examples/hooks/claude-review.sh` and referenced from the README:

```sh
#!/bin/sh
set -eu
skillvendor prompt \
  | claude --bare -p --permission-mode dontAsk --max-turns 1 \
      --output-format json --json-schema "$(skillvendor prompt --schema)" \
  | skillvendor verdict --fail-at medium
```

`--bare` stops Claude Code loading the user's own skills, including the one
under review. `dontAsk` with no allowlist denies every tool call, so the
model can only read what the prompt inlined. A second example,
`static-check.sh`, greps for the obvious patterns and cannot be talked out of
its answer; the README suggests chaining both.

## Implementation notes

- New package `internal/validate`: hook runner (timeout, env, passthrough),
  tree hash, hook hash, cache lookup.
- New package `internal/review`: prompt builder, embedded schema, response
  locator and validator. No network, no exec. Unit-testable with fixture dirs.
- `cmd/skillvendor`: wire `validate`, `prompt`, `verdict`; extend `sync` and
  `syncEntry`; `list` shows `validated` or `unvalidated` per skill.
- README: new "Validation" section after "Sync", covering manifest, contract,
  helper commands and the reference hook.

## Open questions

- Should `sync` print a per-skill changed-files summary on update even with
  no hook configured? Cheap and useful; leaning yes.
- Should `verdict` persist findings (not just tree and hook hash) so `list`
  can show the last review? Deferred until there is a consumer.
