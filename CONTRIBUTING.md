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

Run `go test ./...`. CI runs it on Linux and macOS with `HOME` read-only, plus
`gofmt -l .`, `go vet ./...` and staticcheck, which also reports unused code
(P2). Run the same staticcheck locally with:

    go run honnef.co/go/tools/cmd/staticcheck@v0.8.1 -tags live ./...

- Functional tests build `aits` and run the binary against fake vendors, with
  a temporary `AITS_HOME` and `HOME` and no network or accounts (P12).
- Tests against real vendors are opt-in behind a build tag and are not run in
  default CI.
- The CLI reference is generated from help: after changing a command, run
  `go test ./cmd/aits -run TestCLIDocs -update` (P13).
- `go test -tags live ./cmd/aits -run TestLive` starts and then continues one
  real conversation per agent (Claude Code, Codex) with your own logins: two
  model calls each; `-run TestLive/codex` runs one agent.

The pull-request check script has its own test:

    .github/scripts/check-pr-links-issue_test.sh

## Language

Code, docs, issues, pull requests and commit messages are in English.
