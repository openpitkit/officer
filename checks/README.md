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

`checks/public-packages.txt` is the sorted allowlist of importable packages
and commands, one import path per line. The gate lists the module with
`go list ./...` from the module root, drops `web/node_modules/` (npm install
output, never committed) and every path with an `internal` element, and fails
on any difference in either direction, printing each offending path. A path
`./...` does not match - see `go help packages`; a directory whose Go files are
all excluded by build constraints is skipped too - is neither listed nor
checked.

It runs right after the gofmt check in `lint-go`, `lint-go-debug-dev`, and
`lint-go-release-dev`, so every gate that lints Go runs it, CI included.

Adding a path to the allowlist is a deliberate decision, made together with
the change that adds the package or the command - never a way to make the gate
pass. A library package that is not meant to be imported goes under
`internal/`.
