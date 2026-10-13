# Dependencies

Every direct Go module dependency is listed here with the reason it is needed
(principle P3). CI fails when this table and `go.mod`'s direct requirements
disagree. The project builds with `CGO_ENABLED=0`; a dependency that needs cgo
is not accepted.

| Module | Used by | Why the standard library is not enough | Added in |
| --- | --- | --- | --- |
