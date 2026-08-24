# Checks

Project-specific invariant checks that generic linters (`golangci-lint`,
`eslint`, `tsc`) do not cover. Currently one gate: Semgrep.

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
  semgrep/
    requirements.txt    Semgrep dependency include
    *.yml               every active rule
```

All rules are active. The initial 375 findings from 2026-08-28 have been
remediated, so `just check-semgrep` (and therefore `just check` /
`check-debug` / `check-release` / CI) now blocks only new violations.

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
