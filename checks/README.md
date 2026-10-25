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
    testdata/           regression fixtures, run by semgrep --test
```

All rules are active, and `just check-semgrep` (and therefore `just check` /
`check-debug` / `check-release` / CI) blocks any violation.

`testdata/` holds rule fixtures: deliberate violations and non-violations
annotated for `semgrep --test`, one file per rule, named after it:
`default-domain-status.go`, `locale-latin-words.txt` and
`file-header-site-link.txt`. A fixture runs only
through its own `--test` line in `check-semgrep`, after the tree scan, so a new
fixture adds its own line there. A rule with a fixture keeps its fixture out of
its `paths`, so the tree scan does not report the fixture. The directory is
named `testdata` so the Go tool ignores it, and it is in the justfile's
`go_dirs`, so `lint-go` gofmt-checks the Go fixtures.

Fix a finding by changing the code, or by narrowing the rule if the finding
turns out to be a rule bug - never by loosening what the rule means.

`tailwind-v4-utilities` keeps vertical form spacing on flex-column gaps and
preserves forced-colors focus outlines with `outline-hidden`. It rejects the
earlier utility names whose behavior changed in Tailwind 4.

`unapproved-entity-autocreate` (`checks/semgrep/service-go.yml`) ships without
its `ensure$ANY(...)` branch on purpose: officer's `ensureAccount` /
`ensureLimitAccount` / `ensureAutoCreatedAsset` family in
`framework/node/local_*.go` is the already-reviewed account-lane
auto-provisioning subsystem, not an unapproved fallback. Restoring that branch
is deferred until the account-lane auto-provisioning rework is complete.
`unapproved-entity-autocreate` as it ships today matches nothing in this
repository: it guards new `GetOrCreate` / `FirstOrCreate` call sites and does
not cover any existing automatic-creation site.

`no-test-motive-in-production`
(`checks/semgrep/no-test-motive-in-production.yml`) is a heuristic over prose.
It favours precision, because the product's own
docs talk about paper, testnet and demo modes and a connection test: a singular
"test" after "for" counts only with "only", "purposes" or "use", and "tests"
counts as the subject of a seam verb only at the start of a clause, after
"the" or "our", or after a test kind such as "unit" or "page". Known misses,
which review still catches:

- a bare infinitive after "lets" or "helps" outside the rule's verb list
  ("lets tests advance time", "helps tests inject a fake clock");
- a phrase between "so tests" and its modal ("so tests in the external package
  can reach it");
- "so tests" with a verb and no modal after a seam verb ("Exported so tests
  exercise the parser").

A miss or a false positive found in practice is a rule bug, fixed in the rule.

### `default-domain-status`

`checks/semgrep/default-domain-status.yml` forbids a `default` arm or a map
miss that returns or assigns a valid domain status - a venue, order, fee,
connection, adjustment, market data or audit status - for a value that was not
mapped. Fix a finding by mapping every known value explicitly and returning an
error or reporting a diagnostic for an unknown one. A new domain status type
joins the rule's list, which the rule holds in one place.

Two sites are deliberately not findings:

- `AuditAction.Category` returns an `AuditCategory`, a total classification,
  not a status.
- `marketDataInstanceState` returns `"pending"` for an instance absent from the
  runtime status map, which is what that state means.

This rule has no approval marker.

### `locale-latin-words`

`checks/semgrep/locale-latin-words.yml` forbids a Latin-script word inside a
`ru` or `zh-CN` panel catalog string unless the word is on the rule's keep
list: names, field labels as providers document them, codes and
abbreviations, domain identifiers, and jargon with no established
translation. The keep list is the rule's `keep` group and is the only one; the
localization guidelines point to it. Fix a finding by translating the word
with the term the locale already uses, or, for a term that stays as written in
every translated catalog, by adding it to the keep list. Placeholders, tags,
code spans, figures, URL paths and comma-separated identifier lists such as a
CSV header are not words.

This rule has no approval marker.

### `file-header-site-link`

`checks/semgrep/file-header-site-link.yml` forbids a file header whose
project-site link, in the sentence that points to the OWNERS file, is anything
but `https://officer.openpit.dev`. Fix a finding by writing that link. The rule
excludes only its own fixture, not the rest of `testdata/`, so the other
fixtures' headers are checked too.

This rule has no approval marker.

### Panel access reuse

`checks/semgrep/panel-access-reuse.yml` keeps panel access implementations in
their shared homes: `errorResponse` in `web/src/test/apiClient.tsx`, strict
RFC 3339 decoding in `web/src/framework/api/rfc3339.ts`, new-password inputs
in `PasswordPairFields`, and Show/Hide controls in `RevealButton`. The rules
scan `web/src`, excluding only each shared implementation. They catch the
copied response-and-headers factory, calendar round-trip comparisons,
new-password fields, and translated Show/Hide labels. Their positive and
negative fixtures run as part of `check-semgrep`.

### `no-legacy-database-probe`

`checks/semgrep/no-legacy-database-probe.yml` forbids string literals containing
`pragma_table_info`, `pragma table_info`, `pragma_table_xinfo`, `pragma table_xinfo`
or an `earlier (Officer )?build` phrase, all case-insensitive, in non-test Go under
`internal/store/`. Before writing, startup refuses a database whose shared and
application migration records disagree, either because it was created before the
application tables existed or because its first start was interrupted. That
database must be recreated. Its fixture runs through `check-semgrep`.

This rule has no approval marker.

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
