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
`install-python-tools`, which creates `.venv` and installs
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
annotated for `semgrep --test`, named after the rule with the target language's
extension, for example `discarded-error.go` or `panel-number-input-reuse.tsx`.
Subdirectories mirror the rule directory's structure. After the tree scan,
`check-semgrep` runs `semgrep --test` once over both directories, discovering
matching fixtures automatically. Adding a rule and its fixture requires no
justfile change. A rule with a fixture keeps its fixture out of
its `paths`, so the tree scan does not report the fixture. The directory is
named `testdata` so the Go tool ignores it, and it is in the justfile's
`go_dirs`, so `lint-go` gofmt-checks the Go fixtures.

Fix a finding by changing the code, or by narrowing the rule if the finding
turns out to be a rule bug - never by loosening what the rule means.

`shared-panel-decimal-input` requires decimal fields to use `NumberStepper`.
Raw `Input` and `input` controls with `inputMode="decimal"` bypass its typed
and pasted decimal validation and are rejected outside that shared component.

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

### `account-chain-finally`

`checks/semgrep/account-chain-finally.yml` checks for `Finally` before `Run`
in production Go under `framework/`. It covers inline chains, local short
declarations initialized with `asyncengine.Chain`, and typed `ChainBuilder`
parameters of functions and methods. It exempts a `CheckOrder` chain run inline
or run directly from its local declaration with no write step called on it
before `Run`.

Known misses include reassignments, extra aliases, steps added in another
function, and builders returned by helpers, read from struct fields, or
received as a function-literal parameter. For
split `CheckOrder` chains, a step in an `else`, `for`, `switch`, or closure body
escapes detection. The rule checks the same block and the then-branch of one
enclosing `if`. A `Finally` that ignores its outcome or fails to escalate a
post-commit failure passes the check.

Its fixture runs through `check-semgrep`.

This rule has no approval marker or baseline.

### `error-type-one-form`

`checks/semgrep/error-type-one-form.yml` requires a single probe form for an
error type. A type switch case that lists both `T` and `*T`, or a function
that probes both with `errors.As`, can keep a dead path and obscure which
form the code actually returns. The rule scans production Go across the
repository, excluding `*_test.go` and `testdata` directories.

The switch check compares type spellings within one case. The `errors.As`
check looks for addressed local variables with explicit types in one function
body, in either order. Known misses include type aliases, parenthesised types,
the two forms in separate cases of one switch, a switch or type assertion in
one form with `errors.As` in the other, inferred or inline `errors.As` targets,
targets that are parameters, fields or package variables, and a case list
whose earlier item contains braces. Its fixture runs through `check-semgrep`.

This rule has no approval marker or baseline.

### Internal failures and invalid input

Four rules keep internal failures out of the client validation classification:

- `internal-error-mapper` rejects another package's `Err*` sentinel translated
  to `domain.ErrInvalid` in a single-error mapper's `errors.Is` switch or
  conditional chain. Parsing and input translation belongs at the validation
  call site, including errors from parsing packages, rather than in a general
  single-error mapper.
- `nil-dependency-invalid-input` rejects `domain.ErrInvalid` inside a nil
  guard on a pointer method receiver or its field, including compound conditions.
  Value-receiver DTO validation and request parameter fields are input;
  constructor parameters and field aliases remain
  with review because their role is not syntactically distinguishable.
- `stored-row-invalid-input` rejects explicit `domain.ErrInvalid` in SQLite
  `scan*`/`decode*` helpers and in JSON validation of a scanned column. It also
  rejects a trailing `%w` wrapping `domain.ExternalIDFromBytes` errors for a
  scanned column. Restore transaction methods validate archive input. Input
  reference lookups and shared archive consistency checks may use `Scan`
  without turning their input errors into stored-value errors. Shared DTO
  decoders may propagate validation errors; their stored callers must
  classify those errors as internal.
- `node-internal-invalid-input` checks the post-commit scope helpers and
  errors passed inline or constructed locally for `fatalReconciliation`.

All four rules include `errors.Join` constructions of `domain.ErrInvalid`.
Each rule has a fixture run by `check-semgrep`. Production scans exclude
`*_test.go` and the fixtures. There is no approval marker or baseline.
Aliases, error construction across functions, and arbitrary validators called
on stored values remain with review.

### `store-transaction-helper`

`checks/semgrep/store-transaction-helper.yml` requires SQLite store transactions
to open through `beginTx`, which classifies context-ended transactions while
preserving the original error. Pass the same context to `rollbackTransaction`
so statement and commit errors receive the same classification. Raw `BeginTx`
selectors, including method values, are allowed only inside the helper; tests
and rule fixtures are excluded.
Its fixture runs through `check-semgrep`.

This rule has no approval marker or baseline.

### `store-composite-read-snapshot`

`checks/semgrep/store-transaction-helper.yml` requires one read transaction
when a SQLite `*realmStore` method queries its pool twice or queries it and
passes it to a helper. It matches Go calls after a local `r.db()` assignment,
including calls through a `var reader sqlQueryer = pool` alias after that
declaration. Passing the pool to `beginTx` is allowed. Two hand-offs with no
direct query are allowed because write methods pass the pool to several
helpers. The fixtures run through `check-semgrep`.

The rule skips `Prepare*` reads, other receiver types, and free functions. It
does not follow calls to other methods that open their own pool, or see reads
in a transaction followed by one pool read after commit. It looks for calls
in separate statements at method-body level, so it can miss a pair inside a
single statement, and the `r.db()` assignment and the alias must be direct
statements of the method body; the alias is only the typed `var` form. It does
not track control flow or alias reassignment.
Queries on mutually exclusive paths, or a query paired with a write-only
helper hand-off, can therefore be flagged.

This rule has no approval marker or baseline.

### Limit barrier keys

Keep barrier identity in `domain.LimitKey`: use `domain.NewLimitKey`, a barrier's
`Key()` or `domain.StoredLimitKey`, then compare/index the resulting values.
SQLite predicates use `limitKeyWhere(domain.LimitKey, limitKeyIDs)`. Panel code
uses `limitTarget`, `limitTargetKey` and `sameLimitTarget` from
`web/src/api/limitTarget.ts`.

The rules flag these shapes outside their owners:

- `limit-key-literal`: Go key literals; zero-key error returns need a recognised
  constructor, sentinel, address-of composite literal (`&T{...}`) or non-nil guard.
- `limit-target-literal`: Go target literals except the query-to-DELETE DTO shape.
- `limit-key-string-join`: Go concatenations, `fmt.Sprintf`, `strings.Join` and
  joined map keys with two axes and scope/axis vocabulary or a limit receiver.
- `limit-key-axis-compare`: paired comparisons with a `Scope`/`Axis` field,
  a limit/target-typed owner or a typed-slice range; selector/scalar pairs require
  parameter scalars. Prefix-paired scalar names require `Scope`/`Axis`.
- `limit-key-composite`: inline composite map indexes, equality and `delete` keys.
  They need a `Scope`/`Axis` field, a limit/target-typed owner or a typed-slice range.
- `limit-key-sql`: a heuristic AND-chain check for scope/axis and subject-ID
  placeholders; parenthesised groups are traversed; clause boundaries end the scan.
- `limit-target-template-key`: panel templates, `+`, joins and JSON array keys.
- `limit-target-axis-compare`: paired panel comparisons, including parameter scalars.
- `limit-target-object`: target copies at `deleteLimit` calls or `LimitTarget` annotations.

Panel keys/comparisons need `policy` or a typed `Limit`/`LimitTarget` context,
including typed-array callbacks and `for...of`. Go `fmt.Sprintf` formats with
whitespace are treated as prose. Panel `.join` separators count as keys unless
they are string literals containing whitespace.

Known misses: field-name mismatches; local/destructured scalars; loose-axis call
arguments; keys assigned before indexing and map-literal keys; declaration/new
zero keys; query-to-DTO handler shapes; formatted SQL columns or predicates
without scope/axis; other encodings/accessor chains. Package-level value fields
such as `packageError.ErrCause` still receive the sentinel exemption; parameter
and receiver qualifiers are checked. Mixed member/parameter pairs remain
omitted: they need a new member-position/signature product. The package-level
sentinel field and the mixed member/parameter pair are pinned with `todoruleid`
fixtures.

Known rule bugs marked `todook`: early-success/switch proofs on zero-key error
returns, panel prose templates/concatenations over two axes, and derived Go
operands. A function of an axis, an emptiness test or a length test also matches
as an axis. Go prose built with `+` or `strings.Join` over two axes also fires,
including joins with a whitespace separator.
SQL `OR` in the predicates' enclosing parentheses can also be mistaken for AND
linkage, including inside a subquery.

Run the oracle and tree scan with `check-semgrep`. No approval marker or baseline.

### `test-slog-default-restore`

`checks/semgrep/test-slog-default-restore.yml` forbids direct `slog.SetDefault`
calls in Go test files. Use `internal/logtest.SetDefault(t, handler)` to restore
the previous slog logger, standard log writer and flags at cleanup. Restoring
only slog leaves the standard logger redirected to the finished test's buffer
without its original flags. Tests using the helper must not run in parallel
with other tests that log or capture logs. Its fixture runs through
`check-semgrep`.

This rule has no approval marker or baseline.

### `integration-test-wall-clock-deadline`

The liveness deadline rule covers every Go test in the module. Waits receive
an event or use `polltest.Until` for state; requests use `testCtx(t)` in
`internal/integration` and `t.Context()` elsewhere. A real hang is caught by
`go test -timeout`, which prints every goroutine's stack.

Short windows proving an event did not happen, fake or injected clocks, and
timestamps used as data are exempt. A deadline that is the input under test
stays: `context.WithTimeout` or `context.WithDeadline` inside a function
referencing `context.DeadlineExceeded` is exempt structurally. Success paths
in such a function still use `t.Context()`; only paths asserting expiry keep
the short deadline. Production timeouts and backoffs remain unchanged.

The rule catches request deadlines, clock failure arms in selects (including
timers, deadline channels, and multiple event cases), and wall-clock polling
bounds. Clock arms are checked for failure calls at any statement position and
for error construction with `errors.New` or `fmt.Errorf`; all rule branches
exempt virtual time inside `synctest.Test` bubbles. Its fixtures run through
`check-semgrep`, with no baseline.

### `test-deferred-receive-after-consumed`

`checks/semgrep/integration-test-wall-clock-deadline.yml` also flags a select
arm that consumes a channel value needed by a receive in `defer` or
`t.Cleanup` and then calls `Fatal`, `Fatalf`, `Error` or `Errorf`. Send the
consumed value back before failing so the deferred receive does not hang until
`go test -timeout`. Channels closed anywhere in the enclosing function,
including a nested function or goroutine, are exempt because receiving does
not consume closure. Its fixtures run through `check-semgrep`, with no approval marker or baseline.

### `sqlite-test-parallel`

`checks/semgrep/sqlite-test-parallel.yml` requires SQLite tests to start with
`t.Parallel()` so the race run stays within the package timeout. Tests that
change the process environment, working directory, umask, or default logger
run serially. The companion rule `sqlite-test-parallel-global-state` flags
parallel tests that make one of those changes, including inside a subtest.
Their fixtures run through `check-semgrep`.

Known misses include global mutations one helper call deeper than the listed
names, indirect mutations through code under test, and global mutations in
serial tests with parallel subtests. The companion rule looks for
`t.Parallel()` only among the top-level test's own statements.

These rules have no approval marker or baseline.

### `discarded-error`

`checks/semgrep/discarded-error.yml` forbids all-blank call assignments in
production Go, with any number of blanks and a call on the right, including
untyped `var` declarations. The default
errcheck configuration has `check-blank` disabled, so it accepts these explicit
discards. Return the error, join it into the returned error, or report it
through the existing reporting path. `_ = X.Close(...)` belongs to
`discarded-close-error`; `var _ T = f()` interface assertions and Go test files
are excluded. Receive, index, type assertion and slice right sides are also
excluded, even when they contain calls. Bare calls remain errcheck's
responsibility, including its default exclusions such as `(hash.Hash).Write`,
which never returns an error.
Its fixture runs through `check-semgrep`.

This rule has no approval marker or baseline.

### `discarded-close-error`

`checks/semgrep/discarded-close-error.yml` forbids `_ = X.Close(...)` in
every production Go package, public and internal, including inside deferred
functions. A discarded Close error is a silent fallback: written files,
SQLite handles, and rows iterators can report real failures on Close. Return
the Close error, join it into the returned error, or report it through the
existing reporting path. Tests and rule fixtures are excluded. Pure test
teardown may discard Close errors; a test that relies on the closed resource
(for example, by reopening it or asserting persistence) must check them.
Bare Close calls with an error result are checked by golangci-lint's errcheck.
Its fixture runs through `check-semgrep`.

This rule has no approval marker.

### `test-stub-close-guard`

`checks/semgrep/test-stub-close-guard.yml` flags every call named `Error`,
`Errorf`, `Fatal`, or `Fatalf`, on any receiver, in the failure branch of a
`Read(ctx)`, `Write(ctx, kind, data)`, or `Close(status, reason)` check. The
operation is matched by method name and arity on any receiver, in Go HTTP
test handlers and helpers that take a `*websocket.Conn`.

The rule accepts a nested `if` around the report inside the failure branch.
Its entire condition must be `t.Context().Err() == nil` on the reporting test
or `!harness.isClosing()`.

```go
if err := conn.Close(status, reason); err != nil {
    if t.Context().Err() == nil { t.Error(err) } }
```

Known misses include checks without a direct `!= nil` comparison, reports
routed through another helper, and functions that take neither a
`*websocket.Conn` nor the HTTP handler parameters.

False positives: an overwritten error can tie the check to an earlier call,
and `fmt.Errorf`, `log.Fatal` or `err.Error()` in the failure branch counts as
a report, also inside an accepted guard. Pass `err` with `%v` rather than
`err.Error()`.

This rule has no approval marker or baseline.

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

### `empty-order-status-default`

`checks/semgrep/empty-order-status-default.yml` rejects an empty-status guard
that assigns a replacement to the same status, including a caller-supplied
variable. It covers `OrderStatus` and `Status` fields and locals bound from
`domain.OrderStatus(...)`, in either comparison order. Return an explicit
error for the missing status. Other field names, initialisers, package aliases
and helper-based emptiness checks are outside this syntactic rule.
Its fixture runs through `check-semgrep`; tests and fixtures are excluded
from the production scan. There is no approval marker or baseline.

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
CSV header are not words. The `es` and `pt-BR` catalogs are Latin-script
languages and are covered by `locale-english-words` instead.

This rule has no approval marker.

### `locale-english-words`

`checks/semgrep/locale-english-words.yml` forbids an English phrase inside an
`es` or `pt-BR` panel catalog string. Both languages are written in Latin
script, so the rule reads the English lowercase function words that are no
Spanish or Portuguese word (`the`, `and`, `of`, `with`, `your`, …); any
English sentence or phrase left in a catalog carries one. Fix a finding by
translating the string fully, with the words the locale already uses. Terms on
the `locale-latin-words` keep list are not lowercase function words, so this
rule needs no keep list of its own. Placeholders, tags, code spans and
comma-separated identifier lists are not read.

Deliberate limit: a lone untranslated word or a short label with no function
word ("Settings", "Add policy") passes; review catches it.

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

`checks/semgrep/panel-download-reuse.yml` keeps browser downloads in
`downloadBlob` in `web/src/lib/download.ts`. The rule scans `web/src`, excluding
that file, and catches an anchor created with `document.createElement("a")`
whose `download` is then assigned. Its fixtures run as part of `check-semgrep`.

`checks/semgrep/panel-site-link-reuse.yml` keeps links to `officer.openpit.dev`,
`openpit.dev` and its subdomains built by `siteLink` in `web/src/lib/siteLinks.ts`,
which adds the `officer` visit marker. The rule scans `web/src`, excluding the
helper, test files, `web/src/examples/**` and its fixture. Host examples import
only the published package, so they cannot reach the panel-internal `siteLink`.
Comments are not read. Its fixture runs as part of `check-semgrep`.

### Panel code labels

`checks/semgrep/panel-code-label.yml` keeps every wire code the panel shows -
a status, a side, a source, a reject code, a verdict - behind its catalog
label from `web/src/i18n/codeLabel.ts`. `panel-code-label-raw-field` flags a
code field rendered raw as a JSX child, a `label` or `title`, or a
`BreakdownRow` value; the field names it reads are the panel's wire-code
fields. `panel-code-label-raw-text` flags the same codes held in a variable
whose name ends in one of them (`source`, `sourceFilter`, `providerType`) and
shown as an option `label`, as a filter chip `value` beside a catalog `label`,
or as the value of a `Heading: value` string.
`panel-code-label-silent-fallback` flags a label lookup that falls
back to the raw code without reporting it: a `t()` call whose `defaultValue`
is the code it interpolates, and a registry guard that returns the code
itself. Fix a finding with `codeLabel` or one of its labelers, which show an
unknown code as itself and report it once with `console.warn`. Free text
(`reason`, `detail`) and identifiers (an asset or account id) are not codes
and are not read. The rules scan `web/src` outside tests; their fixture runs
as part of `check-semgrep`.

This rule has no approval marker.

### `no-legacy-database-probe`

`checks/semgrep/no-legacy-database-probe.yml` forbids string literals containing
`pragma_table_info`, `pragma table_info`, `pragma_table_xinfo`, `pragma table_xinfo`
or an `earlier (Officer )?build` phrase, all case-insensitive, in non-test Go under
`internal/store/`. Before writing, startup refuses a database whose shared and
application migration records disagree, either because it was created before the
application tables existed or because its first start was interrupted. That
database must be recreated. Its fixture runs through `check-semgrep`.

This rule has no approval marker.

### Venue connector reuse

`checks/semgrep/venue-connector-reuse.yml` keeps trading stream reconnects in
`streamLifecycle` in `internal/trading/stream_lifecycle.go`. The stream-lifecycle
rule matches the identifier `backoff`, not the package import, elsewhere under
`internal/trading/`.

The HTTP rules keep redirect refusal, client copying and message-and-cause
errors in `internal/vendorhttp`. Elsewhere under `internal/`, excluding Go
tests, the redirect rule rejects `http.ErrUseLastResponse`, every
`CheckRedirect` assignment regardless of its value, and composite-literal
`CheckRedirect` fields other than `vendorhttp.RefuseRedirect`. Use
`vendorhttp.CopyClient` to copy a caller's client without mutating it.
The stream-dial rule rejects every `websocket.Dial` call and copied write-only
stream interface under `internal/`, excluding Go tests and `internal/vendorhttp`.
Use `vendorhttp.DialStream` and `vendorhttp.StreamWriter`. The public,
credential-free feeds in `internal/marketdata/{binance,bybit,okx,kraken,
coinbase}.go` are excluded, and so is `internal/marketdata/finnhub.go`, which
carries its token in the URL and stays outside the rule as existing debt. A new
feed uses `vendorhttp.DialStream` or joins that explicit list in the rule.
The wrap rule rejects a redacted `err.Error()` message paired with its raw
cause in a composite literal or a `vendorhttp.NewRequestError` call. The error
rule rejects a private two-field string/error struct whose `Error` and `Unwrap`
return those fields. Binance keeps its signed-query redaction and passes its
prepared message and cause to `vendorhttp.NewRequestError`. Redacting text alone
and error types with other fields or behavior are allowed. The framework
backend boundary is outside the rules' paths.

`checks/semgrep/no-private-key-formatting.yml` forbids passing ECDSA private
keys or whole credentials values to formatting, logging, error-text construction or JSON
encoding in production Go under `internal/`. Taint sources are values typed as
`*ecdsa.PrivateKey`, `apikey.CDPCredentials`, `CDPCredentials` or `Credentials`,
`PrivateKey` fields and `x509.ParseECPrivateKey` results. Taint follows local
assignments, scalar fields and composite literals. Sinks include `fmt`, `log`,
`slog`, `*slog.Logger`, `errors.New`, JSON marshal calls and encoder `Encode`
calls, plus `String` and `Text` on a private key's `D` scalar. Field selections
other than `PrivateKey` and `D` are safe unless the field itself is typed as a
private key or credentials, allowing the identifier fields sent by venue
protocols, including CDP `APIKeyID` and Binance `APIKey`. Individual HMAC
secret fields of other venues are outside this rule. Function results, including
builtins such as `append` and conversions such as `any(...)`, do not inherit
argument taint, so signatures and JWT subscribe frames remain allowed.
Go tests and rule fixtures are excluded; its fixture runs through `check-semgrep`.

The credential rule keeps vendor credential parsing in `internal/apikey`. It
matches map reads, comparisons and switch arms on `"apiKey"`, `"apiSecret"`,
`"passphrase"`, `"apiKeyId"` and `"apiKeySecret"` outside `internal/apikey/`, and JSON
decoding inside a function named `ParseCredentials` outside that package, in Go
files under `internal/`, excluding Go tests.
Deliberate limits: decoding credentials into a struct tagged with any of the
listed names outside a `ParseCredentials`, and switch arms on named constants
holding those names, pass; review catches both.
Its fixture runs through `check-semgrep`.

These rules have no approval marker.

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
