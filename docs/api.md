# API notes

What a client needs beyond the endpoint reference: where the surface is mounted,
what credentials it takes, what it answers with, and the two error shapes.

## Mount points

REST is mounted at `/api/v1`. A mirror at `/app/api/v1` is reserved for the
operator panel. The two carry the same REST routes except the route manifest,
which is published only on `/api/v1`. They differ in non-release trading
confirmation (see Trading), the credential they take and the source stamped
on the audit record. `/app/api/v1` also carries the panel's own access and
build routes (see Panel access), which are not in the OpenAPI document.
Outside them, MCP over HTTP is mounted at `/mcp`, liveness
at `/healthz`, the OpenAPI document at `/api/openapi.yaml`, its docs page at
`/docs`, and the Swagger UI files under `/docs/` (`GET` only). The dashboard
answers `GET` and `HEAD` on every other path; any other standard method there
answers `404` `not_found`.

Once a non-release build has prepared its database, the access states below
apply as usual. Before that choice is ready, only the panel shell, `/healthz`,
`/docs` and its files, `/api/openapi.yaml`, and the two build routes in
[Panel access](#panel-access) answer normally, behind the same browser checks.
`/mcp` and `GET /api/v1/route-manifest.json` answer `503` `setup_required`;
every other `/api/v1` and `/app/api/v1` path answers a typed `404` `not_found`.

In `setup`, `/api/v1` and `/mcp` answer `503` `setup_required`; the panel's API
admits only its routes marked `duringSetup` (see Panel access). Liveness, the
OpenAPI document, the docs page and its files, and the dashboard still answer.
In the `open` access mode no mount takes credentials. In `token` each takes one
(see Authorization in the [configuration reference](configuration.md)):

- `/api/v1` - the API token, `Authorization: Bearer pit_api_...`;
- `/mcp` - the agent token, `Authorization: Bearer pit_officer_mcp_...`; an
  agent token issued by 0.1 or 0.2, `pit_mcp_...`, stays valid until it is
  regenerated;
- `/app/api/v1` - the panel session cookie, which signing in sets; a bearer
  token is not read there, and the cookie is not read on the other two;
- liveness, the OpenAPI document, the docs page and its files, the dashboard,
  and the public panel access routes - none.

## Discovering the surface

The OpenAPI 3.1 document describes the `/api/v1` surface, the API token
included as its `bearerAuth` scheme. It is served at
`/api/openapi.yaml` and browsable at `/docs`. It is hand-written and embedded in
the binary, and what lives outside `/api/v1` - liveness, the document itself,
the docs page, MCP over HTTP - is not in it.

`/docs` serves Swagger UI from the binary, pinned to the vendored
`swagger-ui-dist` release that `just vendor-swagger-ui` refreshes, under a
content security policy that keeps its scripts, styles and requests on
Officer's own origin. The document at `/api/openapi.yaml` is served from the
binary and needs nothing.

The route manifest, `GET /api/v1/route-manifest.json`, lists every mounted
route - the REST routes on `/api/v1` and `/app/api/v1`, the panel access routes,
MCP over HTTP, liveness, the document, the docs page and its files, and the
dashboard - with its full path and the access it requires in `token` mode
(`public`, `api`, `agent`, or `panel`), and the service's `accessMode`, the
mode in force at the request: `setup`, `open` or `token`. An entry under
`/app/api/v1` carries `duringSetup: true` when its route answers before first-run
setup completes, without credentials; Officer refuses the other routes in `setup`.
The flag does not open the corresponding `/api/v1` route. The REST
entries and the panel access routes that are not public are filtered for the
caller; the others are listed for every caller. The manifest itself requires
`api` access: in `setup` it answers `503` `setup_required`, and in `token` a
request without the API token answers `401` `unauthorized`. The public
`GET /app/api/v1/session` answers the mode without credentials.

## Media types

Control endpoints answer `application/json`. Endpoints that hand back a file -
the backup and business-CSV exports, the service-log download - answer with that
file's own media type as an attachment.

A request other than `GET`, `HEAD`, or `OPTIONS` that carries a body declares
it with one `Content-Type: application/json`, with no parameter but
`charset=utf-8`. Any other body is refused with `415` before it is read; a
request with `Content-Length: 0` needs no `Content-Type`.

## Errors

Two shapes, decided by what failed:

- A malformed request body or a request that violates the schema or a parameter
  constraint answers RFC 9457 `application/problem+json`:
  `{type, title, status, detail, errors: [{code, constraint, pointer}]}`, with
  `400` for broken syntax and `422` for a constraint violation.
- Every other error answers `application/json` with
  `{"error": {"code": "...", "message": "..."}}`.

Three browser checks refuse a request in every access mode, before its
credentials are read and before it reaches a handler, in the second shape:

- `403` `host_not_allowed` - the `Host` header names a host Officer does not
  answer to; see `PIT_OFFICER_ALLOWED_HOSTS` in the
  [configuration reference](configuration.md).
- `403` `origin_not_allowed` - a browser sent a request other than `GET`,
  `HEAD`, or `OPTIONS` from another origin. MCP over HTTP checks the origin
  on every method, `GET` included, since a `GET` there opens an event stream
  and an MCP session. A request carrying neither `Sec-Fetch-Site` nor
  `Origin` passes.
- `415` `unsupported_media_type` - a request other than `GET`, `HEAD`, or
  `OPTIONS` carries a body not declared as JSON (see Media types).

After those checks, until first-run setup completes, every `/api/v1` route
and `/mcp` answer `503` `setup_required`, in the second shape. The panel
mirror does too outside its routes marked `duringSetup`. MCP over HTTP
answers this HTTP error before any JSON-RPC. The message uses the request's
Host in place of `<host>`:

> Pit Officer is not set up yet. Open `http://<host>` in a browser on this
> computer and finish the first-run setup.

In `token` mode the credential check refuses a request next, in the second
shape. MCP over HTTP answers it as this HTTP error, before any JSON-RPC.

- `401` `unauthorized` - no credential, or one Officer does not accept: an
  unknown token, a missing or ended session. On `/api/v1` and `/mcp` it carries
  `WWW-Authenticate: Bearer realm="pit-officer"`; on `/app/api/v1` it carries
  none.
- `403` `wrong_token` - the other surface's token: the agent token on
  `/api/v1`, the API token on `/mcp`. The message names the surface the token
  belongs to.

The panel access routes (see Panel access) add their own, in the second shape:

- `401` `invalid_password` - a wrong password on sign-in, with `Retry-After`;
- `429` `too_many_attempts`, with `Retry-After` - a sign-in during the delay
  after a failed one, which doubles from 1 to 30 seconds and is shared by all
  clients; a recovery request less than a minute after the last code was
  issued, when no code is valid;
- `401` `invalid_recovery_code` - a wrong recovery code, or no valid one;
- `403` `current_password_invalid` - a wrong current password on a switch to
  `open` or a password change; the session stays signed in;
- `409` `conflict` - a route `open` mode does not serve (sign-in, both recovery
  routes, the token routes, and the password change), or a switch to the mode
  already in force;
- `409` `setup_complete` - a setup route after first-run setup completes.

`Retry-After` is the number of seconds until the next attempt is accepted.

## Panel access

The panel's own access routes are mounted only under `/app/api/v1` and are not
in the OpenAPI document. Paths below are relative to that prefix.
`GET /build`, `POST /build/database`, `GET /session`, `POST /session`, and the
two recovery routes are public; the rest take the panel session in `token`
mode. The database-choice gate serves only the two build routes before ready.

- `GET /build` - `200` `{release, phase, releaseDatabaseExists, error?}`,
  without a session, including during setup. A non-release build reports phase
  `choice`, `preparing`, `failed` or `ready`; `error` is present only in
  `failed`, and `releaseDatabaseExists` is meaningful during `choice` and
  `failed`. Once ready, the normal application reports
  `{release: false, phase: "ready", releaseDatabaseExists: false}`. A release
  build serves the same route with `release: true`, `phase: "ready"` and
  `releaseDatabaseExists: false`.
- `POST /build/database` - `{fromRelease: bool}` chooses a release snapshot
  (`true`) or an empty database (`false`); `202` with the build status while
  preparation runs. It exists only before ready in a non-release build.
  While this route exists, a choice outside `choice` and `failed` answers
  `409` `conflict`; choosing a copy
  without a release file answers `422` `validation`. Missing or null
  `fromRelease` answers a `422` problem response with constraint `required` at
  `/fromRelease`. Stop the release build before choosing a copy; see
  [Non-release builds](configuration.md#non-release-builds).
- `GET /session` - `{accessMode, setupComplete, authenticated}`;
  `setupComplete` is `false` exactly in `setup`, and `authenticated` is whether
  the request carries a live session, always `false` in `setup` and `open`.
- `POST /session` - sign in with `{password}`; sets the session cookie.
- `DELETE /session` - sign out; clears the cookie.
- `DELETE /sessions` - end every session; clears the cookie.
- `POST /session/recovery` - print a recovery code to Officer's standard error
  (see Authorization in the [configuration reference](configuration.md));
  answers `{issued, expiresAt}`, with `issued` `false` and that code's expiry
  while an earlier code is still valid.
- `POST /session/recovery/reset` - `{code, password}`: set the password with
  the code; ends every session and sets a new cookie.
- `GET /access` - `{mode, passwordSetAt, sessions}`; `passwordSetAt` is `null`
  in `open`, `sessions` the number of live sessions.
- `PUT /access/mode` - `{"mode": "token", "password": "..."}` sets the password,
  issues both tokens and sets the cookie;
  `{"mode": "open", "currentPassword": "...", "acknowledged": true}` deletes the
  password, both tokens and every session and clears the cookie.
- `PUT /access/password` - `{currentPassword, password}`; ends every other
  session.
- `GET /access/tokens/{kind}` - `{token}`, the current value; `kind` is `api`
  or `agent`.
- `POST /access/tokens/{kind}/regenerate` - `{token}`, a new value; the old one
  stops working at once.

The four setup routes take no credentials in `setup`:

- `GET /setup` - `200` `{step, encryptionDeclinedAt}`; `step` starts at
  `welcome` and is one of `welcome`, `accounts`, `encryption`, `signing` or
  `access`. `encryptionDeclinedAt` is the first refusal's time in RFC 3339 UTC,
  or `null` before one. Account mode, sealing and signing are read through
  their own routes.
- `PUT /setup/progress` - `{step}`, one of those five values; `204`. It saves
  a position, including the same one again, without checking step order or
  earlier decisions. Another value answers `422` `validation`.
- `PUT /setup/encryption` - `{"encryption": "none", "acknowledged": true}`;
  `204`. A repeat keeps the first refusal's time. Another encryption value or
  a false acknowledgement answers `422` `validation`; a sealed database
  answers `409` `conflict`. The master key is configured at process startup;
  a sealed database takes precedence over a stored refusal.
- `POST /setup/complete` - `{"access": "open", "acknowledged": true}` or
  `{"access": "token", "password": "..."}`; `204`. Completion requires an
  account mode choice, a stored `spot_funds_limit_mode` row (see Spot Funds
  limit mode), and either a sealed database or a recorded refusal of
  encryption; missing decisions answer `409` `conflict` with a message naming
  them. `token` sets the panel password, issues both tokens and sets the session
  cookie. The response carries no token values; the session can read them
  through `GET /access/tokens/{kind}`. Another access value, a false
  acknowledgement in `open`, or a password outside 8 to 256 characters in
  `token` answers `422` `validation`. A non-null `password` in `open` or
  `acknowledged` in `token` answers `422` `validation`; `null` counts as absent.

After completion all four answer `409` `setup_complete`, subject to the panel
session check in `token` mode. They are panel-only, outside OpenAPI, as are the
session and access routes above.

In `setup`, `GET /build`, `GET /session` and these four routes answer without
credentials.
The other panel access routes answer `503` `setup_required`, including the
public sign-in and both recovery routes. The panel mirror also admits the
routes marked `duringSetup`: account-mode reads and choices, Spot Funds
limit-mode reads and choices, service restart,
credential-storage status, the account list, signing-key generation, import,
list and public-key reads, and signing-config reads and changes. Every other
route on the mirror answers `503` `setup_required`.

A new password - `password` in `POST /setup/complete` to `token`, in
`PUT /access/mode` to `token`, in `PUT /access/password`, and in
`POST /session/recovery/reset` - is 8 to 256 characters, and one outside that
range answers `422` `validation`; a sign-in
password and `currentPassword` are only compared with the stored one. The
session cookie is `pit_officer_session`, set with
`Path=/; HttpOnly; SameSite=Strict; Max-Age=2592000`. A session ends 30 days
after sign-in or 7 days after its last request, and survives a restart.

## Realized account P&L reset

The standalone host advertises `features.accountPnlReset` in `GET /account-mode`.
When installed, `POST /accounts/{code}/realized-pnl/reset` force-sets whole-account
realized P&L to zero, including halted/unset values. Positions, cost basis and
history are retained; kill switches remain latched. Missing accounts are refused.

`GET` and `PUT /account-pnl-reset-schedule` read/edit one daily all-account job.
Writes require `enabled`, `time` (HH:mm), `timezone` (IANA), `termination` and
`revision`. Termination is `{"mode":"forever"}`, `{"mode":"count","maxRuns":100}`
or `{"mode":"until","until":"2030-12-31T23:59:00Z"}`. Revision zero creates the
first job; edits retain its ID and successful-run count. Responses contain `job`
(null when absent) and `runtimeError`. Job status includes next execution and
last outcome with independent account results. Recovery uses
`POST /account-pnl-reset-schedule/recover` with current `revision` and explicit
`action` (`retry` or `skip`). Agents are always refused for mutations. MCP exposes
only `get_account_pnl_reset_schedule`. Manual resets apply account scope; schedule access requires
global authority. In standalone Officer, operators can configure the schedule in
both account modes; it resets every stored account, including accounts hidden by
single-account mode. Manual resets remain scoped to the visible account.
Missed boundaries are skipped visibly. Backup/restore of reset
job progress with receipts requires `accounts_groups` in the same selected scope.
See [daily realized P&L reset](configuration.md#daily-realized-pl-reset) for
count, end, DST, receipt identity and crash semantics, and the native Go
[`scheduler` package](https://pkg.go.dev/go.openpit.dev/officer/framework/scheduler)
for the generic host contract.

## Values

Money and size values cross the wire as exact decimal strings, not as IEEE 754
floats.

The order, trade, account and group lists bound their filter values, in REST
and in the MCP list tools alike; a character here is a Unicode code point. A
decimal bound is written in at most 64 characters and must be representable in
the engine's decimal: at most 28 fractional digits, trailing zeros aside, and
its digits, read as one integer, at most 79228162514264337593543950335. A zero
written with an exponent - its e-part less its fractional digits - outside
-92..28 is refused. A text filter takes at most 16 fragments and 4096
characters across them, the longest text Officer stores; a fragment containing
NUL is refused, and an order status list names no status twice. REST answers a
value outside these limits with `422` and code `validation`, the MCP list tools
with `validation: <message>`.

## MCP tools

The MCP server answers `initialize`, over stdio and over HTTP, with
`serverInfo.name` `pit-officer` and `serverInfo.version` the Officer version: on
a release build the release tag without its leading `v` (`0.2.1` for
`v0.2.1`), and `dev` on a non-release build - a local binary or image, `edge`
or a release dry run (see [Building without Just](development.md#building-without-just)).
The engine version is the `health` tool's `node.engine.version`.

The tool catalog is served by the API and shown in the operator panel. It
records, per tool, what it does, whether it mutates state, and whether it is
implemented - a catalogued name that is not implemented is listed but not
callable.

Non-mutating tools are enabled by default. Every tool that changes state ships
disabled, stays disabled until an operator enables it by name, and refuses the
call while it is off.

An error Officer reports for a tool call answers a text of the form
`<code>: <message>`, with the same code and message the REST API answers for
that error (see Errors) - including a value Officer rejects (`validation`) and
a refused trading access (`forbidden`). Two errors also list in the text the
members REST returns with them: `destination_ambiguous` its candidate
connections, as `; <connection> (<label>)`, and `has_dependents` its
dependents, as `; <kind> (<count>)`. Plain text comes back only from a check the
tool makes before it calls Officer - a required argument left empty, or an
argument the tool parses itself, except in the list tools (below) - and from
the MCP gate's own refusals: an unauthorized caller, a failed command-access
check, and a disabled command.

The two test-policy tools, `get_test_execution_policy` and
`set_test_execution_policy`, return a different tool-error shape after argument
decoding: `isError: true`, the public REST error body in `structuredContent`, and
the error's detail or message as text. Validation pointers and constraints are
preserved. Protocol errors and command-gate refusals keep their existing shapes.

The core answers a tool call that would loosen risk state - lift a block, drop
or weaken a barrier, reset a rate window - with `loosening_refused` and changes
nothing; REST and the operator panel are not affected. `delete_group` is
refused for a blocked group and for a group with a group-scoped Spot Funds P&L
bound, with or without `force`, and for any group while a rate-limit barrier is
configured, as the delete rebuilds the engine and so resets every rate window.
`delete_account`, with or without `force`, and a code change in
`update_account` are refused for a blocked account, an account in a blocked
group or in a group with a group-scoped Spot Funds P&L bound, and an account
carrying a barrier of its own, as an account recreated under the freed code
would have none of them; `delete_account` is also refused while a rate-limit
barrier is configured. `create_account` is refused, and so is
`missingAccount=create` for an account that does not exist yet in
`apply_adjustment`, `add_limit`, `submit_order` and
`submit_drop_copy_order`, as a new account starts unblocked, with no barrier
of its own and no balance or P&L history. For keyed policies, `update_limit`
returns `not_found` if the barrier or account is absent. `apply_adjustment` may
only lower the balance, held or incoming of the account's own currency, and may not
change the average entry price or
the realized P&L: another asset - a position included - an account without a
currency of its own, a delta above zero, an absolute above the field's current
value and an `averageEntryPrice` or `realizedPnl` other than the current one
are refused, and so is any `realizedPnl` while the position's P&L calculation
is halted, as it would restart it. An `id` starting with `trading:` is refused
too, as that namespace is reserved for the trading runtime. `delete_asset`
without `force` and a code change in
`update_asset` are refused while a rate-limit, order-size or position barrier
references the asset or a Spot Funds P&L bound is denominated in it, and `delete_asset`
with `force` is refused whatever the asset carries, as it would delete the
asset's dependent rows with it - balances, orders, trades, adjustments and
barriers among them.

`add_limit` creates a barrier at a new address. An existing key returns
`limit_exists` and leaves the barrier unchanged. A new account-tier order-size
cap must be no looser than the reject asset-tier cap it overrides. For every
policy with a rule mode, `add_limit` and `update_limit` address only the reject
barrier and refuse a `mode` argument. Review barriers are read-only for an
agent. MCP deletes no barrier.
A new account or group bound must use the currency of the bound it takes over
from and be at least as strict. The first reject order-size barrier is refused
while a rate-limit barrier is configured, as installing it rebuilds the engine
and resets every rate window.

`update_limit` changes the barrier at an existing address. A missing key returns
`not_found`. Use `add_limit` to create it. An agent's change must keep the
barrier at least as strict. A rate limit can admit no more orders, and its
window must stay the same on a broker or asset scope or be at least as long on
an account or account_asset scope. Keep every cap and bound, and keep the
currency of each P&L bound. Caps and upper bounds can only stay the same or
decrease. Lower bounds can only stay the same or increase. For `position_limit` the ceiling
can only stay the same or decrease.
`update_limit` with policy `spot_funds_limit_mode` accepts `enforce` and
`track_only` as arguments. It refuses a switch down to `track_only` with
`loosening_refused` and either review value with `validation`. Set the
review values through REST or the panel.
Trading and market data may only be narrowed: `grant_trading_access`,
`create_trading_connection`,
`set_trading_instrument`, `delete_trading_instrument`,
`set_trading_fee_account` and `clear_trading_fee_account` are always refused,
as are an enable through `set_trading_connection_enabled` or
`set_market_data_instrument`, a credential change through
`update_trading_connection`, which restarts the trading runtime, and a
`revoke_trading_access` after which no account can send through a connection
with no fee account on a provider with unlinked fees while exactly one could
before, as the connection's venue fees would then be skipped instead of
debited; any other revoke, a disable and a label-only update pass.

### List tools

`list_orders`, `list_trades`, `list_accounts` and `list_groups` are read-only
and enabled by default. Each returns one page of the rows the matching REST
list returns, under the same key with the total:
`{"orders": [...], "total": N}`, and likewise `trades`, `accounts` and
`groups`.

They take the filters the REST lists apply and check them with the core
list-filter rules, the limits under Values included; each tool's description
names its filters and sort columns.
REST checks the same values in its own query parser, so its spellings differ -
an omitted argument here stands for REST's `all` - and so do its 422 texts.
The JSON shapes:

- text match: `{fragments, anchorStart, anchorEnd}`;
- count, decimal and time ranges:
  `{min, max, equal, notEqual, minExclusive, maxExclusive}`, decimals as exact
  decimal strings and times as RFC 3339; a time range has no `equal` or
  `notEqual`;
- sort: `{column, descending}`.

The trading-connection filter is
`venue: {"mode": "any" | "none" | "connection", "connection": "<id>"}`: rows
through at least one connection, through none, or through the named one
(`connection` goes with that mode only). The link it reads is an order's own
venue link, the venue link of a trade's order, an account's own grant or its
group's, and a group's own grant.

Paging takes `limit` (default 50, capped at 500) and a 1-based `page`; `total`
counts the matching rows before paging. On the first page `list_groups` also
returns the default group when it matches the filters, outside the limit and
the total.

A value Officer rejects answers `validation: <message>`, including the tools'
own argument checks, such as a time that is not RFC 3339 or a negative limit or
page. An argument that does not decode into its type, such as a string for
`limit`, is rejected by the MCP layer before the tool runs and comes back as a
protocol error, not as a tool result.

### Control-plane tools

These tools map to the following REST operations:

- `create_account`, `update_account`, `delete_account`: `POST /accounts`,
  `PUT /accounts/{code}`, `DELETE /accounts/{code}`;
- `set_account_currency`: `PUT /accounts/{code}/currency`;
- `get_account_pnl_reset_schedule`: `GET /account-pnl-reset-schedule`;
- `apply_adjustment`: `POST /accounts/{code}/adjustments`;
- `create_asset`, `update_asset`, `delete_asset`: `POST /assets`,
  `PUT /assets/{code}`, `DELETE /assets/{code}`;
- `create_group`, `update_group`, `delete_group`: `POST /groups`,
  `PUT /groups/{code}`, `DELETE /groups/{code}`;
- `add_limit`: `POST /limits/rate`, `POST /limits/order-size`,
  `POST /limits/position`, `POST /limits/spot-funds-pnl-bounds`,
  `POST /limits/spot-funds-unrealized-pnl-bounds` or
  `POST /limits/spot-funds-total-pnl-bounds`, by its `policy` argument;
- `update_limit`: `PUT /limits/rate`, `PUT /limits/order-size`,
  `PUT /limits/position`, `PUT /limits/spot-funds-pnl-bounds`,
  `PUT /limits/spot-funds-unrealized-pnl-bounds`,
  `PUT /limits/spot-funds-total-pnl-bounds` or
  `PUT /limits/spot-funds-limit-mode`, by its `policy` argument;
- `report_fill`: `POST /orders/{id}/execution-reports`.

For keyed barriers, `update_limit` returns `not_found` when the key is absent.
REST `PUT` creates the barrier in that case.

The fourteen mutating tools are protective and ship disabled. The path code or id
is the `account`, `asset`, `group` or `order` argument, a delete's `force` query
parameter is the `force` argument, `missingAccount` keeps its query name, and
body fields keep their REST names; `apply_adjustment` takes the whole REST body
as its `adjustment` argument. `add_limit` and `update_limit` take the fields of
the corresponding REST body as flat arguments next to `policy`, omitting the
barrier's rule mode. A field of another policy is refused with `validation`.
Only `update_limit` accepts `spot_funds_limit_mode` and its `mode` argument.
`get_limits` and `get_account_state` return the write fields and the read-only
`mode` of every rate, order-size, position and P&L-bound barrier.
A rate barrier's window is `windowMs` in integer milliseconds, as both write
tools take it, in place of the `window` duration string
they answered before. `get_limits` without an account also answers the stored
Spot Funds limit-mode row in `spotFundsLimitModes`; the row is global, so a
list narrowed to an account, `get_account_state` among them, never carries it.
The `update_*` tools replace the code and title,
`update_asset` also the asset class, as `PUT` does.

Arguments reach the core as given, and the core decides every value rule, the
single-account refusal and which accounts are visible, as for REST. A tool
answers any code its REST operation documents - most often `validation`,
`not_found`, `conflict`, `accounts_not_managed`, `has_dependents`,
`currency_change_blocked`, `terminal_order`, `venue_order_managed` or the
retryable `engine_restarting` - except that the default group `-` given as
`group` is refused with `validation`, where REST answers `reserved_group`.

A REST `currency_change_blocked` error also carries `reasons` when the guard
reports blocking facts. Each fact names the affected `account` and its `kind`:
`available`, `held`, `incoming`, `average_entry_price`, `realized_pnl`,
`balance_pnl_halt`, `account_pnl`, `account_pnl_halt`, `active_order` or
`pnl_limit`. Balance facts include `asset` and exact `value`; halt facts carry
the engine's reason code in `value`. Orders include their external `order` ID
and `status`. Limits include `scope`, `axis`, `currency`, the owning `group`
where applicable, and the set `lowerBound` and `upperBound`. These facts come
from the same read that refused the operation, including every blocking member
of a group. The panel translates reason kinds and keeps decimal strings exact.

A tool answers the body its REST operation answers: `{"account": ...}`,
`{"asset": ...}`, `{"group": ...}`, `{"adjustment": ...}`, `{"rateLimit": ...}`,
`{"orderSizeLimit": ...}`, `{"positionLimit": ...}`,
`{"spotFundsPnlBoundsLimit": ...}`,
`{"spotFundsUnrealizedPnlBoundsLimit": ...}`,
`{"spotFundsTotalPnlBoundsLimit": ...}`, `{"mode": ...}` or the
execution-report body. A delete, which REST answers without a body, answers its
own input. An adjustment the engine rejects, such as one whose resulting balance
leaves its bounds, is recorded and answered with its `rejected` outcome, as REST
answers it; one that changes nothing answers `{}`, where REST answers 204.

`report_fill` leaves a report on a drop-copy order unattested; a report on an
ordinary order is attested, and signed unless eSign-off is set. REST's `force`
is not offered, and a present `commission` is passed as given: REST reads an
all-empty commission as none, the core refuses it.

## Order submission

Mode `immediate` books the fill at the order's own `price`. An order without
`price` in `immediate` is refused before the order is recorded, the account is
created or the engine reserves: `POST /orders/submit` answers `422` with code
`validation` and constraint `required` at pointer `/price`, the MCP tool
`submit_order` answers `validation: <message>`. The refused attempt is audited
as `approval_failed`. A market order goes in `hold`, and its fill arrives as an
execution report. This is an incompatible REST and MCP change in 0.1: before
it, a market order in `immediate` settled at the engine's reservation lock
price.

## Order review

A review-mode rule can return `verdict: review` with `reviews` (the rule marks
and their `mode`). The order waits in `awaiting_review` without being sent.
Funds are held if and only if the review has at least one mark and every mark
is `review_with_hold`; any
`review_without_hold` mark means nothing is held. `review.held` on
`GET /orders/{id}` is the authority. Any verdict other than `accept` means
**do not send**. Submit and send-paper/send-live return the marks; check returns
them without mutation.

`GET /orders/{id}` includes `review` only while waiting: `held`, `marks`, the last
failed confirmation (`lastConfirm`, or null), and the stored trading
`destination` (or null for a gateway order). A gateway polls this endpoint
after a review verdict. A `pre_trade_accepted` event attested as `review_confirm`
or `review_force` means approved to send (`committed` in hold mode, `filled` or
`partially_filled` in immediate mode); `cancelled` means do not send.

`POST /orders/{id}/review/confirm` has no body and returns the order, a signed
`decision` (`accept` or `reject`) and `venueSend` only for an accepted trading
order. A rejection is HTTP 200 and leaves the order waiting; retry uses the
same endpoint. A trading pre-check can return 409 for a disabled connection or
instrument or stopped runtime, 403 for an unbound connection or different venue
account, or 404 for a missing instrument mapping while the order keeps waiting.
Every documented error except 500 occurs before the decision and leaves the
order unchanged. After a 500 the decision may already be recorded; check
`GET /orders/{id}` for its state before retrying.
Every confirmation runs pre-trade again and spends rate-limit
units again. `POST /orders/{id}/review/cancel`, also without a body, returns
the cancelled order and its signed attestation, releasing a held review's hold.
Execution reports and the ordinary `/confirm` and `/cancel` shortcuts refuse
waiting orders with 409
`conflict`. MCP shows review status only; it cannot confirm, retry, force or cancel.

`POST /orders/{id}/review/force` has no body and accepts only held waiting
orders, without a pre-trade run. It returns an `accept` decision and the order:
`committed` in hold mode (with `venueSend` for a trading order), `filled` in
immediate mode. A review without hold or an account blocked by its own flag or
group returns 409 `conflict`. A blocked order keeps waiting with its hold;
lift the block before forcing it. A trading force has the same binding and venue
pre-check refusals as confirm (403, 404, 409, leaving the order waiting) and the
same 500 rule: check `GET /orders/{id}` before retrying.

Submit, send-paper, send-live, review confirm, force and review cancel responses
may include `alertFailure` with `step` (`publish` or `close`) and `message`, only
when the order decision committed but its alert step failed. The status code is
unchanged. This member is absent from the ordinary `/confirm` and `/cancel`
shortcuts and from event reproduction. See [Actionable alerts](alerts.md) for
the review alert and its lifecycle.

## Backups

A release build copies the database before a migration. `GET
/database/pre-migration-backups` lists these raw SQLite copies; `POST
/database/pre-migration-backups/{id}/download` downloads one and `DELETE
/database/pre-migration-backups/{id}` deletes it. Both download and delete
are audited. Officer never deletes a copy itself. See
[Migrations and upgrades](configuration.md#migrations-and-upgrades).

An archive exported by a non-release build has
`nonRelease: true` in its manifest. A release build refuses it with `422`
`non_release_archive` before decoding its data, so an `edge` backup restores
only into a non-release build.

A restore in a non-release build disables enabled trading connections it
restores. Its summary names those disabled for this reason in
`tradingDisabledNonRelease`; a connection disabled because its credentials
could not be opened is named in `tradingCredentialsUnavailable` instead. The
OpenAPI document describes the restore request and summary.

## Trading

Trading credentials are JSON objects accepted on write; no trading route or
MCP tool returns them. A backup export carries the stored credentials according
to its `credentialForm`: sealed under a master key, plaintext without one, as with
market-data credentials. Modes are `paper` and `live`, and both are created,
enabled and restored alike, with or without a master key. `GET /credential-storage` and
the MCP tool `get_credential_storage` report whether stored credentials are
sealed under the master key and, when they are not, whether trading connections
or market-data sources hold keys in plain text. `GET /credential-storage` also
reports `sealedAt`, when the database was sealed, and `resealedSecrets`, how
many stored secret values that sealing re-sealed - `null` and `0` for a
database never sealed; the MCP tool reports neither.
An instrument mapping whose base or quote asset Officer does not know creates
that asset, as a market-data instrument mapping does; each created asset is
audited as `create_asset`. An asset code Officer cannot accept is refused with
`validation`, and an unknown connection or market-data source with `not_found`;
neither creates an asset.

In a non-release build, creating a trading connection enabled or enabling an
existing one requires `nonReleaseConfirmed: true`, accepted only on the panel
mirror `/app/api/v1`. Without panel confirmation the server answers `403`
`non_release_confirmation_required`. `/api/v1` and MCP cannot enable trading;
disabling needs no confirmation. A release build ignores the member. In the
panel, enabling opens a warning dialog; only **Enable trading** in that dialog
sends the confirmation.

A venue-linked order is managed by its venue connection. Manual execution
reports, confirm and cancel refuse it with `venue_order_managed`. Trading MCP
tools follow the catalog rule above: mutating tools ship disabled.

Access to a connection is granted to an Officer account or an account group:
`PUT` and `DELETE /trading/connections/{id}/access` take `account` or
`accountGroup`, as do the MCP tools `grant_trading_access` and
`revoke_trading_access`, though a grant through MCP is refused with
`loosening_refused` (see MCP tools). A member account sends through its
group's grant; the account's own grant on the same connection wins. On
providers with unlinked fees a connection without a fee account books them to
the one account that can send through it; when none or several can, each fee
is skipped with a `venue_fee_skipped` audit. A group grant, or a grant to a
second account, needs the fee account there and is refused with
`fee_account_required` without one.

Paper and live sends require `destination.route`, a JSON object. The built-in
Alpaca, Binance, IB, eToro and OKX providers require `amountKind: "quantity"`
and support only the `timeInForce` route field. Values are case-sensitive and
are never normalized:

- Alpaca requires `day`, `gtc`, `opg`, `cls`, `ioc` or `fok`. Good till cancelled
  is `{"timeInForce":"gtc"}`; uppercase `GTC` is refused locally with the
  allowed spellings, before an order or reservation is created or an order is
  sent to the venue. Crypto allows only `gtc` or `ioc`; a fractional equity
  quantity requires a fractionable asset and `day`.
- Binance requires `{"timeInForce":"GTC"}`, `{"timeInForce":"IOC"}` or
  `{"timeInForce":"FOK"}` for a limit order and `{}` for a market order.
- OKX requires `{"timeInForce":"GTC"}`, `{"timeInForce":"IOC"}` or
  `{"timeInForce":"FOK"}` for a limit order and `{}` for a market order.
- IB requires `{"timeInForce":"day"}`, `{"timeInForce":"gtc"}` or
  `{"timeInForce":"ioc"}`. Crypto buys require a limit price; crypto market
  buys are unsupported.
- eToro uses `{}` for market orders or `{"timeInForce":"ioc"}` for an IOC
  limit buy. Limit sells are unsupported.

The `ib` provider trades through a running TWS or IB Gateway session over the
TWS API. Its credentials are `{host, port, clientId?}`: host and port are
required, and Officer derives a client ID from the connection when none is
given. These values are not secret; the connection status returns them under
`settings`, and the panel prefills its settings dialog from them. The route is
`{"timeInForce": "day" | "gtc" | "ioc"}`. An IB order goes out only through a
grant bound to a venue account, the IB account ID; a grant without one cannot
carry an order. Commissions arrive with their fills. IB returns executions only
since local midnight: when reconciliation needs older executions, the order
stays pending and the connection shows a diagnostic. It
never treats the order as missing and never infers a fill.

The `okx` provider requires `{apiKey, apiSecret, passphrase, site}` credentials.
All four fields are required. `site` is the OKX site the account is registered
on: `www.okx.com` for global accounts, `my.okx.com` for EEA accounts or
`app.okx.com` for US and Australian accounts. The connection status returns
`site` under `settings`, never API key, API secret or passphrase values. Paper
mode uses OKX demo trading with demo API keys; live mode uses live trading. Only
spot cash trading is supported, with no venue account binding.

Execution Simulator (provider `test`) is a paper-only venue that takes credentials
`{}` and sends no requests to external venues.
It accepts quantity orders with `destination.route: {}` and uses Officer asset
codes directly, without an external symbol mapping. Its saved execution rules
are checked from top to bottom; the first matching mask wins. An unknown symbol
is refused with `test_policy_no_match`. Only orders sent to this connection
are processed; a normal `hold` submission stays open. `GET` and
`PUT /trading/connections/{id}/test-policy` read and replace that policy through
REST; `get_test_execution_policy` and `set_test_execution_policy` do the same
through MCP. The policy write alone permits an agent to replace test rules;
other trading setup remains subject to the restrictions above. An account may
access any combination of provider kinds and paper/live modes. Effective access
includes own and group grants, including disabled connections. A successful
change that first creates mixed paper/live access publishes an account-named
warning alert. Repeated writes while already mixed do not repeat it; removing
the mixed access and later restoring it publishes another alert. The panel
shows a current warning beside its effective venue count and in its access dialog.

Account `venueAccess` sorting orders by the effective number of distinct
connections, combining direct and inherited grants, including disabled
connections and deduplicating overlap. The row's `venueAccess` flag and the
`any`/`none` filters retain their boolean meaning. Mixed-access alert reads and
acknowledgement use the installed account scope; hidden events contribute no
unread or total count. Delivery completes synchronously after administrative
locks and restore runtime completion. Warning-only failures use
`trading_access_notification_failed`; joined operational completion failures use
`trading_access_completion_failed` and require service reconciliation as well.
Acknowledging the alert records awareness and changes no access or orders.
If notification fails after persistence, `409`
`trading_access_notification_failed` explicitly reports that access was saved;
review current access and alerts before retrying the mutation.
See the [test trading guide](test_trading.md) for setup and examples.

Paper sends use `POST /orders/send-paper` or `send_paper_order`; live sends use
`POST /orders/send-live` or `send_live_order`, with the same checks and outcomes.
`acknowledged` means the venue accepted. `refused` means a definitive refusal by
the venue or Officer provably did not hand it the order, recorded cancelled with
leaves 0 and reservation released; `error` says why.
`unknown` means the call could not establish the outcome or read the stored link,
or the venue refused but cancellation is not recorded yet or could not be
confirmed; `error` says why. `GET /orders/{id}` is authoritative, and the runtime
reconciles orders that reached the send step, retrying cancellation after a
refusal.
MCP results for `submit_order`, `send_paper_order` and `send_live_order` also
include the optional `alertFailure` member when the committed order's alert step
fails.

## Adding and editing keyed limits

Send `POST` to add a barrier at a new key or `PUT` to create or replace the
barrier at that key. Both methods accept the same request body and
`missingAccount` query parameter on these paths:

- `/limits/rate`
- `/limits/order-size`
- `/limits/position`
- `/limits/spot-funds-pnl-bounds`
- `/limits/spot-funds-unrealized-pnl-bounds`
- `/limits/spot-funds-total-pnl-bounds`

Both return the stored barrier, with `201 Created` for POST and `200 OK` for PUT.
A POST to an existing key returns `409` with code `limit_exists` and leaves the
barrier intact. Use PUT to edit the barrier.

Every body above accepts `mode` with one of the values `reject`,
`review_without_hold` or `review_with_hold`. An absent or empty mode means
`reject`. An unknown mode returns `422` with code `validation` at `/mode`.
The mode is part of the key, so a reject and a review barrier can share a
target. Changing a barrier's mode requires deleting it and adding a barrier
with the new mode.

An SDK validation refusal during runtime configuration returns `422` with code
`validation` and the SDK's message. An SDK refusal while registering a built-in
policy during a first-barrier engine build returns `500`.

`DELETE /limits` accepts `mode` for each of these policies. An absent or empty
mode addresses the reject barrier. An unknown mode returns `422 validation`.

`GET /limits` accepts a non-empty `mode` filter with a specific policy
(`rate`, `order_size`, `position`, or one of the three P&L-bound policies).
An absent or empty mode leaves the list unfiltered by mode. `total` counts the
filtered barriers before paging. A non-empty mode with an unsupported policy
or an unknown mode is refused with `422 validation`. Every barrier row carries
its rule `mode`. Only `spot_funds_limit_mode` carries `mode: null`, with its
fund-control setting under `values.spotFundsLimitMode.mode`.

## Spot Funds P&L bounds

For scope `account`, a realized, unrealized or total P&L bound must use the
account's effective currency, resolved from the account, its group, then the
default group. This includes the currency of the account a
`missingAccount=create` request would create, which inherits the default
group's currency. If no currency resolves, the bound is refused. REST returns
`409` with code `pnl_bound_currency_mismatch`. MCP `add_limit` and
`update_limit` return the same code and message. The rule does not apply to
`account_group` or `global` scope. With `missingAccount=reject`, REST and
`add_limit` return `account_missing` for an unknown account (HTTP `404`
in REST). Barriers already stored are not re-checked.

An unrealized or total P&L bound needs a price for every asset the account
holds: an order of an account that holds an asset without a price is rejected
and blocks the account, as the realized bound does when it cannot calculate the
P&L.

## Position limit

The SDK's SpotFunds policy enforces a non-negative position ceiling for each
(account, asset) pair; Officer stores exact decimal strings and computes no
position or decision. A buy adds the order to the owned position
(`available + held`) plus open positive `incoming`. A sell subtracts the order
from the owned position less open positive `held`. A buy above the ceiling or
a sell below minus the ceiling triggers the barrier, while equality passes.
The barrier refuses the order in `reject` mode and requests review in either
review mode.
The reject is policy `SpotFundsPolicy`, scope `order`, code
`position_limit_exceeded`, reason "position limit exceeded", with details
`asset {asset}, side long|short, projected worst-case position {projected}, limit {limit}`;
the account is not blocked.

`POST /limits/position` and `PUT /limits/position` take
`{scope, account, asset, maxPosition, mode}` with scope `account_asset`, a
required asset, an optional rule mode and a required non-negative exact decimal
`maxPosition` in plain notation; zero is valid. A negative or non-decimal
ceiling answers `422` `validation`. `?missingAccount=create|reject` is
required when an account is named, choosing whether to register a missing
account or refuse with `404`. In single-account mode an omitted or empty
account stands for that account. The answer is
`{"positionLimit": {scope, account, asset, maxPosition, mode}}`, read back as
stored.

`GET /limits` lists the barrier as kind `position_limit`, scope `account_asset`,
with `values.position.maxPosition`; `policy=position` selects it alone and
`policy=all` includes it. The account answer includes `limits.positionLimits`,
always an array, and account-scoped callers see only their visible accounts'
pairs. `DELETE /limits?policy=position_limit&scope=account_asset&account=acc-1&asset=BTC`
clears the reject barrier for that pair and answers `204`, or `404` when no
reject barrier is stored. Pass `mode` to clear a review barrier.

Every change applies from the next order without a restart. The stored set is
what every engine build takes, a restart and a backup restore included.
Position barriers apply in all four Spot Funds fund-control modes.

## Spot Funds limit mode

The Officer-level policy `spot_funds_limit_mode` decides whether the Spot Funds
policy refuses an order for lack of funds or holdings. It is not an engine
policy: Officer stores it and applies it to the running engine. It takes the
`global` scope only and covers every account; there is no per-account or
per-group mode. With no row stored the effective mode is `enforce`, the
documented default. First-run setup completes only once the row is stored; a
database set up before that requirement may have none.

- `enforce` refuses an order that lacks funds or holdings.
- `review_with_hold` requests review on a funds shortfall and holds the full
  amount, which can take available funds negative. A confirmed order retains
  the hold.
- `review_without_hold` requests review on a funds shortfall without holding
  funds while review is pending. Confirmed and drop-copy orders use
  `track_only` without a review mark. Overflow still rejects.
- `track_only` does not refuse on funds but records every hold, so balances and
  positions may go negative: an account trades from zero without prepared
  balances.

P&L-bound, rate, order-size and position barriers apply in all four modes
according to their own rule modes.

`GET /limits/spot-funds-limit-mode` answers
`{mode, stored, accountsWithoutCurrency}`. `stored` is whether the row is
stored; with none, `mode` is `enforce`. `accountsWithoutCurrency` is always an
array: the codes of visible accounts
without an effective currency to which a Spot Funds P&L bound applies. Their
P&L halts, so the bound blocks them. The panel shows them as a warning while the
mode is `track_only`.

`PUT /limits/spot-funds-limit-mode` takes `{scope, mode}` with scope `global`,
applies it live without a restart or an engine rebuild, and answers
`{"spotFundsLimitMode": {scope, mode}}` read back as stored, without the
no-currency list. A body naming an account, account group or asset is refused
with 422. Switching back to `enforce` resets no balance; orders that need funds
against a negative balance are refused again.

`GET /limits` always lists one row of kind `spot_funds_limit_mode` on
the `global` scope, with `values.spotFundsLimitMode.mode`. When no setting is
stored, the row shows the effective default `enforce`;
`policy=spot_funds_limit_mode` selects it alone.
`DELETE /limits?policy=spot_funds_limit_mode&scope=global` is refused with 422:
the policy is permanent. Change its mode through `PUT` instead.

For tighten-only writes, the core ranks `enforce` above both equally ranked
review modes, and both review modes above `track_only`. A switch to a lower
rank is refused with `loosening_refused`. The MCP tool `get_spot_funds_limit_mode`
can return any of the four values.

The stored mode is what every later engine build takes, a restart and a backup
restore included: the mode travels with the backup's general settings, and a
restore applies it to the running engine.

The panel lists it on the Policies page as one permanent global policy row.
Edit that row to change the setting. Other policies are selected through the
Orders, P&L, and Confirmation and review groups. Each policy links to its wiki
section.

## Single-account mode

In single-account mode every client request acts on one default account. The
mode is stored and read once, when the process starts. `GET /account-mode`
answers the stored `mode` (`single`, `multi`, or `null` until chosen, which
behaves as `multi`) and `account`, the `features` the running process enforces,
`restartRequired`, and `live`. `PUT /account-mode` stores the next mode. Before
the first-run setup is complete, while `live` is `true`, the running process
takes it at once and the answer carries `live: true`; otherwise it leaves the
running process unchanged, and the change takes effect after
`POST /service/restart`. A host that runs an account scope of its own always
answers `live: false`. `single` takes the named account, which must exist; with
none named, the one existing account, or a new account `default` when there is
none. Several accounts without one named are refused with 422 with constraint
`required` at pointer `/account`, and a running single-account mode accepts
only its own account (409 `accounts_not_managed`: put `multi` into effect
first). A database reset or a backup restore that changes the stored choice is
reported by `restartRequired` too.

While the mode runs:

- An `account` omitted from a request body, a query parameter or an MCP input
  means the default account. A route that carries the account in its path, such
  as `GET /accounts/{code}` or `POST /accounts/{code}/adjustments`, still takes
  it there: the client sends the code `features.account` reports.
- With the account omitted, `missingAccount` is not needed: an omitted policy is
  `reject`, as the default account always exists, except for a drop-copy, which
  can never refuse a missing account and always uses `create`, in either mode.
  With the account named, in the path or as `account`, the default account
  included, `missingAccount` is required as in multi-account mode (422 with
  constraint `required` at pointer `/missingAccount`).
  Paper submission (`POST /orders/send-paper`, `send_paper_order`) is an
  exception: it does not use `missingAccount` and never creates an account;
  the account must already exist.
- Another account is refused with 422 with constraint `single_account_mode` at
  pointer `/account`, any account group with 422 with constraint
  `single_account_mode` at pointer `/accountGroup`, and an order of another
  account is not found. The `account` query parameter of the balance, adjustment,
  trade and audit lists and of `GET /limits` is a text filter, not an account
  input: it narrows the rows the mode shows, so another account's code answers
  200 with no rows. The `account` match of the MCP `list_trades` tool is also a
  text filter and returns no rows for another account's code.
- Creating or deleting an account, `PUT /accounts/{code}` even on the default
  account, setting an account's group, any group change, and the default-group
  currency are refused with 409 `accounts_not_managed`. The default account's
  currency, notes and block state change as usual.
- Lists and counts show the default account's rows and rows of no account;
  groups, trading grants and fee accounts of other accounts are hidden. Venue
  executions and fees still post to the account they belong to.
- Creating a trading connection, and enabling one, grants the default account
  access to it when the account has none, its own or its group's; the system
  writes the grant and the audit records it as such. A refused grant is the
  error of that create or enable, whose own change is already stored and
  started. The `ib` provider is the exception: its grant names the IB account
  ID orders go through, which the client chooses among the accounts the
  gateway manages, so the client writes that grant with
  `PUT /trading/connections/{id}/access`.

The mode narrows what clients see, not what they may do: backup export, backup
restore and `POST /database/reset` act on every account, and any client can
switch the mode.

The stdio MCP server is a process of its own: it reads the mode when it starts,
so it sees a change only once its host restarts it. MCP tool input schemas, over
stdio and over HTTP, follow the mode in effect when the session opens; in
single-account mode `account` and `missingAccount` are optional and
`accountGroup` is not a property. An HTTP session opened before the mode in
effect changed - a database reset returns Officer to the first-run setup, which
takes a mode at once - ends: its next request answers 404, and the client opens
a new session.

## Audit source

Every control-plane action is recorded with the surface it arrived on. The
source is derived from the mount point that served the request, never from a
client-supplied header or body: `/api/v1` stamps the API source and
`/app/api/v1` the panel source, so a client selects the recorded source by
selecting the path.
