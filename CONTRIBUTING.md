# Contributing

Read [the principles](docs/principles.md) first. Every review checks changes
against them.

## Workflow

1. **Open an issue first.** Use the *Feature* template. It must stand on its
   own: the problem, a concrete use case (who runs what, with which input, and
   gets which result), acceptance (exact commands, expected output, the main
   failure cases) and what is explicitly out of scope. A new command group,
   or a change that adds a network dependency, says how it meets each of the
   five conditions of P15 (a toolbox for agents, not a platform).
2. **One pull request per issue.** Put `Closes #<issue>` in the description;
   CI fails without it. Keep the pull request to what the issue asks for.
3. **Ship it together.** Code, CLI help, docs, the agent skill
   ([skills/aits/SKILL.md](skills/aits/SKILL.md)) and tests land in the same
   pull request (P13). Any change to what a caller sees (a command, flag,
   output field, status, error or default) updates the skill.
4. **Report what you ran.** The pull request lists the commands you ran and
   their actual results, and says what was not verified.

## Clean-room rule

aits is an independent implementation. An earlier private implementation
exists and informs behaviour and lessons only. Never copy its code, and never
put its private paths, commit ids, file names or internal names in issues,
pull requests, commits or docs. When an issue deliberately leaves out a
behaviour the earlier implementation had, say so and why.

## Tests

Run `go test ./...`. CI runs it on Linux and macOS with `HOME` read-only, plus
`gofmt -l .`, `go vet ./...` and staticcheck, which also reports unused code
(P2), and checks that the release binary stays within
`.github/binary-size-budget` (P14); a change that needs more raises the
budget in the same pull request and says why. Run the same staticcheck locally with:

    go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./...

- Functional tests build `aits` and run the binary against fake vendors, with
  a temporary `AITS_HOME` and `HOME` and no network or accounts (P12).
- Tests against real vendors are opt-in behind a build tag and are not run in
  default CI.
- The CLI reference is generated from help: after changing a command, run
  `go test ./cmd/aits -run TestCLIDocs -update` (P13).
- `go test ./cmd/aits -run TestSkill` fails when the skill does not name
  every command, flag and result status; fix the skill, not the test.
- `go test -tags live ./cmd/aits -run TestLive` starts and then continues one
  real conversation per agent (Claude Code, Codex) with your own logins: two
  model calls each; `-run TestLive/codex` runs one agent.

The pull-request check script has its own test:

    .github/scripts/check-pr-links-issue_test.sh

## Language

Code, docs, issues, pull requests and commit messages are in English.
