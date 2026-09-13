# Checks

Project-specific invariant checks that generic linters (`golangci-lint`,
`eslint`, `tsc`) do not cover. Two gates: Semgrep, and the public package
allowlist.

## Semgrep invariant gate

Deterministic rules for domain invariants, scanned over the whole repository
(every text file - Go, TS/TSX, Markdown, YAML, JSON, justfiles - not just one
extension list).

Run it directly:

```bash
just check-semgrep
```

It is also a dependency of `lint-all`, so `just check` / `just check-debug` /
`just check-release` run it automatically. In CI it runs as part of
`just --justfile pipeline.just ci-go`. `check-semgrep` depends on
`install-semgrep`, which creates `.venv` and installs
`requirements.txt` on first run - no manual setup step needed.

### Layout

```text
checks/
  README.md            this file
  public-packages.txt  the public package allowlist
  semgrep/
    requirements.txt    Semgrep dependency include
    *.yml               every active rule
```

All rules are active, and `just check-semgrep` (and therefore `just check` /
`check-debug` / `check-release` / CI) blocks any violation.

Fix a finding by changing the code, or by narrowing the rule if the finding
turns out to be a rule bug - never by loosening what the rule means.

`unapproved-entity-autocreate` (`checks/semgrep/service-go.yml`) ships without
its `ensure$ANY(...)` branch on purpose: officer's `ensureAccount` /
`ensureLimitAccount` / `ensureAutoCreatedAsset` family in
`framework/node/local_*.go` is the already-reviewed account-lane
auto-provisioning subsystem, not an unapproved fallback. Restoring that branch
is deferred until the account-lane auto-provisioning rework is complete.
`unapproved-entity-autocreate` as it ships today matches nothing in this
repository: it guards new `GetOrCreate` / `FirstOrCreate` call sites and does
not cover any existing automatic-creation site.

### Approval marker

`unapproved-entity-autocreate` is not an absolute prohibition. The construct
is allowed when the team agreed to it; the rule catches the *unagreed* case.
Record agreement on the line above:

```go
// fallback(approved): <what is substituted and why this was agreed>
```

Never use the marker to silence a false positive - a false positive means the
rule needs narrowing, not a marker.

## Public package gate

`checks/public-packages.txt` lists, one import path per line and sorted,
exactly the set the gate compares it with: the paths `go list ./...` reports
from the module root with cgo enabled for the host platform, except those under
`web/node_modules/` or with an `internal` path element. It is meant to hold the
importable packages and the commands. The gate runs `go list ./...` from the
module root, so it sees every package `./...` matches there, not only what the
justfile builds. For what `./...` excludes, see `go help packages`; `./...` also
skips a directory whose Go files are all excluded by build constraints, which
that page does not name. A package the pattern does not match is neither listed
nor checked, and listing one fails the gate.
The gate skips `web/node_modules`, which is npm
install output rather than part of the module, keeps every package that is not
under an `internal` path element, `main` packages included, and fails on a
difference in either direction: such a package that is not listed, or a listed
path that is not such a package. It prints every such path.

It runs right after the gofmt check in `lint-go`, `lint-go-debug-dev`, and
`lint-go-release-dev`, so every gate that lints Go runs it, CI included. Run it
with `just lint-go`.

Adding a package or a command to the allowlist is a deliberate decision, made
together with the change that adds the package or the command - never a way to
make the gate pass. A library package that is not meant to be imported goes
under `internal/`.
