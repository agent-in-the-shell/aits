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
