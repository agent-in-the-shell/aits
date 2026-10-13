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
