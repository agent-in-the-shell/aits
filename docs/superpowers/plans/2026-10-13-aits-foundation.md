# aits Foundation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the public `agent-in-the-shell/aits` repository with its principles, contributor workflow, issue/PR templates and the CI check that every pull request links an issue — no Go code.

**Architecture:** Plain Markdown documents at the repo root and under `docs/`; GitHub templates under `.github/`; one POSIX-shell check script with its own shell test, run by a GitHub Actions workflow on pull requests. The repo is created and pushed with `gh`.

**Tech Stack:** Markdown, bash, `grep -E`, GitHub Actions, `gh` CLI.

**Spec:** `docs/superpowers/specs/2026-10-13-aits-foundation-design.md` (section 4 is the deliverable list; section 3 is the principles text).

## Global Constraints

- All repo text is English.
- No Go code, no `go.mod` in this plan (spec §4: code-level guards land with the first feature issue).
- Public text never contains the reference's private paths, commit IDs, file names or internal names (P11).
- License: MIT, copyright "The aits Authors".
- Repo: `agent-in-the-shell/aits`, public, default branch `main`.
- Storage root wording, where mentioned: `$AITS_HOME`, default `~/.local/share/aits`.
- Supported platforms wording, where mentioned: Linux and macOS on amd64 and arm64.

## Review Focus

1. A PR description using another GitHub closing keyword (`Fixes #12`, `resolves: #3`, lowercase `closes #4`) — a contributor expects it to pass.
2. A PR description that only *mentions* an issue (`see #12`, `Closes #`, `Closes owner/other#12`) — it must fail; it links nothing in this repo.
3. An empty PR description (GitHub sends `null`/empty) — must fail with a readable message, not a script error.
4. A PR description containing shell metacharacters (`` `cmd` ``, `$(…)`, quotes) — must be treated as data, never executed.
5. A PR description edited after opening to add `Closes #n` — the check must re-run and pass.

Items 1–4 are cases in `check-pr-links-issue_test.sh` (Task 2); item 5 is the `edited` trigger plus the live verification in Task 3.

---

### Task 1: Repository documents

**Files:**
- Create: `README.md`
- Create: `LICENSE`
- Create: `docs/principles.md`
- Create: `docs/dependencies.md`
- Create: `CONTRIBUTING.md`
- Create: `AGENTS.md`
- Create: `CLAUDE.md`

**Interfaces:**
- Consumes: spec §3 text.
- Produces: `docs/principles.md` with headings `### P1 — …` through `### P13 — …` (Task 2's templates reference principles by `P<n>` ids); `CONTRIBUTING.md` section `## Workflow` (linked from the PR template).

- [ ] **Step 1: Write `README.md`**

```markdown
# aits

Run coding agents from the command line: dispatch work to vendor agent CLIs,
queue and follow up on it, search past agent sessions, and route LLM requests
through an OpenAI-compatible gateway — one Go binary, built for AI agents to
call.

> **Status:** pre-release. Nothing is usable yet; the foundation is being laid
> issue by issue.

## Planned command groups

| Group | Job |
| --- | --- |
| `aits shell` | Run a prompt through a vendor agent CLI (Claude Code, Codex) and return the result and session id |
| `aits task` | A local work queue: create tasks, run them through `shell`, follow up on the same session |
| `aits session` | Full-text search over vendor transcripts; get back a resumable session id |
| `aits model` | OpenAI-compatible LLM gateway with routing and fallback |

## Design

- [Principles](docs/principles.md) — the rules every change follows, each with
  the failure it prevents and the check that enforces it.
- [Contributing](CONTRIBUTING.md) — issue first, one pull request per issue.

## License

MIT — see [LICENSE](LICENSE).
```

- [ ] **Step 2: Write `LICENSE`**

```text
MIT License

Copyright (c) 2026 The aits Authors

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

- [ ] **Step 3: Generate `docs/principles.md` from spec §3**

The principles text is spec §3 verbatim, under a document title. Extract it mechanically so the two cannot diverge at creation:

```bash
{
  printf '# Principles\n\n'
  printf 'The design rules for aits. Approved in\n'
  printf '[the foundation spec](superpowers/specs/2026-10-13-aits-foundation-design.md);\n'
  printf 'change them only through an issue.\n\n'
  awk '/^## 3\. Principles$/{on=1; next} /^## 4\. /{on=0} on' \
    docs/superpowers/specs/2026-10-13-aits-foundation-design.md \
    | sed 's/^### /## /'
} > docs/principles.md
```

- [ ] **Step 4: Verify the extraction**

Run: `grep -c '^## P[0-9]* — ' docs/principles.md && head -12 docs/principles.md && tail -5 docs/principles.md`
Expected: count `13`; the head shows the title, intro, then the "Each principle states the rule…" paragraph and `## P1 — One binary, …`; the tail ends with the P13 **Guard** paragraph (no `## 4.` heading).

- [ ] **Step 5: Write `docs/dependencies.md`**

```markdown
# Dependencies

Every direct Go module dependency is listed here with the reason it is needed
(principle P3). CI fails when this table and `go.mod`'s direct requirements
disagree. The project builds with `CGO_ENABLED=0`; a dependency that needs cgo
is not accepted.

| Module | Used by | Why the standard library is not enough | Added in |
| --- | --- | --- | --- |
```

- [ ] **Step 6: Write `CONTRIBUTING.md`**

```markdown
# Contributing

Read [the principles](docs/principles.md) first. Every review checks changes
against them.

## Workflow

1. **Open an issue first.** Use the *Feature* template. It must stand on its
   own: the problem, a concrete use case (who runs what, with which input, and
   gets which result), acceptance (exact commands, expected output, the main
   failure cases) and what is explicitly out of scope.
2. **One pull request per issue.** Put `Closes #<issue>` in the description;
   CI fails without it. Keep the pull request to what the issue asks for.
3. **Ship it together.** Code, CLI help, docs and tests land in the same pull
   request (P13).
4. **Report what you ran.** The pull request lists the commands you ran and
   their actual results, and says what was not verified.

## Clean-room rule

aits is an independent implementation. An earlier private implementation
exists and informs behaviour and lessons only. Never copy its code, and never
put its private paths, commit ids, file names or internal names in issues,
pull requests, commits or docs. When an issue deliberately leaves out a
behaviour the earlier implementation had, say so and why.

## Tests

There is no code yet. From the first feature onward:

- Functional tests build `aits` and run the binary against fake vendors, with
  a temporary `AITS_HOME` and `HOME` and no network or accounts (P12).
- Tests against real vendors are opt-in behind a build tag and are not run in
  default CI.

The pull-request check script has its own test:

    .github/scripts/check-pr-links-issue_test.sh

## Language

Code, docs, issues, pull requests and commit messages are in English.
```

- [ ] **Step 7: Write `AGENTS.md`**

```markdown
# Working in this repo (for coding agents)

1. Read [docs/principles.md](docs/principles.md) and
   [CONTRIBUTING.md](CONTRIBUTING.md) before changing anything.
2. Work from an issue. If the task has no issue, write one with the Feature
   template before coding.
3. Cite principles by id (`P1`–`P13`) when you justify a design choice in an
   issue or pull request. If a change would break or loosen a principle, say
   which one and why, and stop for a human decision.
4. Never copy code from, or name private details of, any reference
   implementation (CONTRIBUTING, "Clean-room rule").
5. Report results honestly: the commands you ran, their actual output, and what
   you did not verify.

Design specs and plans live in `docs/superpowers/specs/` and
`docs/superpowers/plans/`.
```

- [ ] **Step 8: Write `CLAUDE.md`**

```markdown
# CLAUDE.md

Read [AGENTS.md](AGENTS.md); it is the entry point for every coding agent in
this repo.
```

- [ ] **Step 9: Check relative links resolve**

Run:

```bash
for f in README.md CONTRIBUTING.md AGENTS.md CLAUDE.md docs/principles.md docs/dependencies.md; do
  grep -oE '\]\([^)#]+\)' "$f" | sed -E 's/^\]\(//; s/\)$//' | grep -v '^https\?://' | while read -r link; do
    target="$(dirname "$f")/$link"
    [ -e "$target" ] || echo "BROKEN: $f -> $link"
  done
done; echo done
```

Expected: only `done`, no `BROKEN:` lines.

- [ ] **Step 10: Check for private details**

The term list is private, so it never lives in the repo. The controller provides
it as a file outside the repo and exports its path as `AITS_LEAK_TERMS`
(one extended regex per line). Stop and ask if the variable is unset.

```bash
test -s "${AITS_LEAK_TERMS:?set AITS_LEAK_TERMS to the private term-list file}" &&
git ls-files --others --cached --exclude-standard | xargs grep -n -i -E -f "$AITS_LEAK_TERMS"; echo "exit=$?"
```

Expected: no matching lines and `exit=1` (grep found nothing).

- [ ] **Step 11: Commit**

```bash
git add README.md LICENSE CONTRIBUTING.md AGENTS.md CLAUDE.md docs/principles.md docs/dependencies.md
git commit -m "docs: add readme, license, principles and contributor workflow"
```

---

### Task 2: Templates and the pull-request issue-link check

**Files:**
- Create: `.github/scripts/check-pr-links-issue.sh`
- Test: `.github/scripts/check-pr-links-issue_test.sh`
- Create: `.github/workflows/pr.yml`
- Create: `.github/ISSUE_TEMPLATE/feature.md`
- Create: `.github/pull_request_template.md`

**Interfaces:**
- Consumes: `CONTRIBUTING.md#workflow` (Task 1), principle ids `P1`–`P13`.
- Produces: `check-pr-links-issue.sh` — reads the PR description from env var `PR_BODY`; exit `0` and prints `ok: links #<n>` when it contains a GitHub closing keyword (`close`, `closes`, `closed`, `fix`, `fixes`, `fixed`, `resolve`, `resolves`, `resolved`; any case; optional `:`) followed by whitespace and `#<digits>`; otherwise exit `1` and prints to stderr `pull request description must link an issue, e.g. "Closes #12"`.

- [ ] **Step 1: Write the failing test**

`.github/scripts/check-pr-links-issue_test.sh`:

```bash
#!/usr/bin/env bash
# Tests for check-pr-links-issue.sh. Run from anywhere.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
check="$here/check-pr-links-issue.sh"
fail=0

expect() { # expect <want-exit> <name> <body>
  local want="$1" name="$2" body="$3" got
  PR_BODY="$body" "$check" >/dev/null 2>&1
  got=$?
  if [ "$got" -ne "$want" ]; then
    echo "FAIL $name: want exit $want, got $got"
    fail=1
  else
    echo "ok   $name"
  fi
}

expect 0 closes            'Closes #12'
expect 0 lowercase         'closes #4'
expect 0 fixes             'Fixes #12'
expect 0 resolves-colon    'resolves: #3'
expect 0 closed-past       'Closed #7'
expect 0 in-paragraph      $'Adds the parser.\n\nCloses #12\n\nTested with make.'
expect 1 empty             ''
expect 1 mention-only      'see #12'
expect 1 no-number         'Closes #'
expect 1 other-repo        'Closes owner/other#12'
expect 1 keyword-in-word   'encloses #12'
expect 1 no-space          'Closes#12'

# Shell metacharacters in the body are data, never executed.
marker="$(mktemp -u)"
expect 1 metachar-no-link  "\$(touch $marker) \`touch $marker\`"
expect 0 metachar-link     "\$(touch $marker) Closes #5"
if [ -e "$marker" ]; then
  echo "FAIL metachar: body was executed"
  rm -f "$marker"
  fail=1
fi

# An unset PR_BODY fails cleanly with the readable message.
msg="$(env -u PR_BODY "$check" 2>&1 >/dev/null)"
case "$msg" in
  *'must link an issue'*) echo "ok   unset-body" ;;
  *) echo "FAIL unset-body: got '$msg'"; fail=1 ;;
esac

exit "$fail"
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `chmod +x .github/scripts/check-pr-links-issue_test.sh && .github/scripts/check-pr-links-issue_test.sh; echo "exit=$?"`
Expected: `FAIL` lines for the exit-0 cases (the check script does not exist, so every run exits 127) and `exit=1`.

- [ ] **Step 3: Write the check script**

`.github/scripts/check-pr-links-issue.sh`:

```bash
#!/usr/bin/env bash
# Fails unless the pull request description (env PR_BODY) links an issue in
# this repo with a GitHub closing keyword, e.g. "Closes #12" (principle P11).
set -u
body="${PR_BODY:-}"
pattern='(^|[^[:alnum:]_])(close[sd]?|fix(e[sd])?|resolve[sd]?):?[[:space:]]+#[0-9]+'
if match="$(printf '%s\n' "$body" | grep -o -i -E "$pattern" | head -n 1)" && [ -n "$match" ]; then
  echo "ok: links #${match##*#}"
  exit 0
fi
echo 'pull request description must link an issue, e.g. "Closes #12"' >&2
exit 1
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `chmod +x .github/scripts/check-pr-links-issue.sh && .github/scripts/check-pr-links-issue_test.sh; echo "exit=$?"`
Expected: every line starts with `ok`, and `exit=0`.

- [ ] **Step 5: Write the workflow**

`.github/workflows/pr.yml`:

```yaml
name: pr

on:
  pull_request:
    types: [opened, edited, reopened, synchronize]

permissions:
  contents: read

jobs:
  links-issue:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Test the check script
        run: .github/scripts/check-pr-links-issue_test.sh
      - name: Description links an issue
        env:
          # Passed through env, never interpolated into the script (P8/P9: untrusted input).
          PR_BODY: ${{ github.event.pull_request.body }}
        run: .github/scripts/check-pr-links-issue.sh
```

- [ ] **Step 6: Write the issue template**

`.github/ISSUE_TEMPLATE/feature.md`:

```markdown
---
name: Feature
about: Propose a change. Every change starts here.
title: ""
labels: []
---

## Problem

<!-- What is missing or wrong today, from the caller's point of view. -->

## Use case

<!-- Who runs it, with what input, which command, and what result they get. -->

## Acceptance

<!-- Exact commands and expected output, including the main failure cases
(invalid input, missing dependency, timeout, ...). -->

## Out of scope

<!-- What this issue deliberately does not do. -->

## Not carried over

<!-- Behaviours an earlier implementation had that this issue deliberately
leaves out, and why. Write "none" if none. -->

## Principles

<!-- Principles this touches (P1–P13), and any it would loosen. -->
```

- [ ] **Step 7: Write the pull request template**

`.github/pull_request_template.md`:

```markdown
Closes #

## What changed

## Verification

<!-- Commands you ran and their actual results. Say what you did not verify. -->

## Principles

<!-- Principles touched (P1–P13). See CONTRIBUTING.md#workflow. -->
```

- [ ] **Step 8: Re-run the test and the private-details check**

Run: `.github/scripts/check-pr-links-issue_test.sh && git ls-files --others --cached --exclude-standard | xargs grep -n -i -E -f "$AITS_LEAK_TERMS"; echo "exit=$?"`
Expected: all `ok` lines, no grep matches, `exit=1`.

- [ ] **Step 9: Commit**

```bash
git add .github
git commit -m "ci: require pull requests to link an issue; add issue and PR templates"
```

---

### Task 3: Publish and verify on GitHub

**Files:** none (remote operations only).

**Interfaces:**
- Consumes: the committed `main` from Tasks 1–2; workflow job `links-issue`.
- Produces: public repo `agent-in-the-shell/aits` with `main` pushed.

- [ ] **Step 1: Confirm the repo name is free and the tree is clean**

Run: `gh repo view agent-in-the-shell/aits 2>&1 | head -1; git status --short`
Expected: `Could not resolve to a Repository …`; empty status.

- [ ] **Step 2: Create the public repo and push**

```bash
gh repo create agent-in-the-shell/aits --public \
  --description "Run coding agents from the command line — one Go binary built for AI agents to call." \
  --source . --remote origin --push
```

Expected: `✓ Created repository agent-in-the-shell/aits`, `✓ Pushed commits`.

- [ ] **Step 3: Verify default branch and files**

Run: `gh repo view agent-in-the-shell/aits --json visibility,defaultBranchRef --jq '.visibility, .defaultBranchRef.name' && gh api repos/agent-in-the-shell/aits/contents --jq '.[].name'`
Expected: `PUBLIC`, `main`; the listing includes `.github`, `AGENTS.md`, `CLAUDE.md`, `CONTRIBUTING.md`, `LICENSE`, `README.md`, `docs`.

- [ ] **Step 4: Open a verification PR without an issue link — expect failure**

```bash
git switch -c chore/verify-pr-check
git commit --allow-empty -m "chore: verify the pull request check (do not merge)"
git push -u origin chore/verify-pr-check
gh pr create --title "Verify PR check (do not merge)" --body "Verification only; no issue link."
gh pr checks --watch; echo "exit=$?"
```

Expected: `links-issue` **fail**; the job log shows `pull request description must link an issue`; non-zero exit.

- [ ] **Step 5: Edit the description to link an issue — expect the check to re-run and pass**

```bash
gh pr edit --body "Verification only. Closes #999"
sleep 5; gh pr checks --watch; echo "exit=$?"
```

Expected: a new `links-issue` run triggered by `edited`, **pass**, `exit=0`. (`#999` need not exist; the check only verifies the link syntax, and the PR is closed unmerged.)

- [ ] **Step 6: Close the verification PR and clean up**

```bash
gh pr close --delete-branch --comment "Verification complete; closing without merge."
git switch main
git branch -D chore/verify-pr-check 2>/dev/null; git status --short; git log --oneline -1
```

Expected: PR closed, remote branch deleted, local `main` clean.
