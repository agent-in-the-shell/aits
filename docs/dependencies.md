# Dependencies

Every direct Go module dependency is listed here with the reason it is needed
(principle P3). CI fails when this table and `go.mod`'s direct requirements
disagree. The project builds with `CGO_ENABLED=0`; a dependency that needs cgo
is not accepted.

| Module | Used by | Why the standard library is not enough | Added in |
| --- | --- | --- | --- |
| `modernc.org/sqlite` | `aits-gw` (`internal/gw`) only; never reaches `aits` (P1 guard) | The event record needs a durable, transactional store with a unique (kind, source_id) constraint and `user_version` migrations (P7); the standard library has no database engine, and this one is pure Go, so builds keep `CGO_ENABLED=0` | #165 |
