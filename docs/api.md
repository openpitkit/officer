# API notes

What a client needs beyond the endpoint reference: where the surface is mounted,
what credentials it takes, what it answers with, and the two error shapes.

## Mount points

REST is mounted at `/api/v1`. A mirror at `/app/api/v1` is reserved for the
operator panel. The two carry the same REST routes except the route manifest,
which is published only on `/api/v1`; otherwise they differ only in the
credential they take and the source stamped on the audit record. `/app/api/v1`
also carries the panel's own access routes (see Panel access), which are not in
the OpenAPI document. Outside them, MCP over HTTP is mounted at `/mcp`, liveness
at `/healthz`, the OpenAPI document at `/api/openapi.yaml`, its docs page at
`/docs`, and the Swagger UI files under `/docs/` (`GET` only). The dashboard
answers `GET` and `HEAD` on every other path; any other standard method there
answers `404` `not_found`.

In `setup`, `/api/v1` and `/mcp` answer `503` `setup_required`; the panel's API
admits only its routes marked `duringSetup` (see Panel access). Liveness, the
OpenAPI document, the docs page and its files, and the dashboard still answer.
In the `open` access mode no mount takes credentials. In `token` each takes one
(see Authorization in the [configuration reference](configuration.md)):

- `/api/v1` - the API token, `Authorization: Bearer pit_api_...`;
- `/mcp` - the agent token, `Authorization: Bearer pit_mcp_...`;
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
`GET /session`, `POST /session`, and the two recovery routes are public; the
rest take the panel session in `token` mode.

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
  account mode choice and either a sealed database or a recorded refusal of
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

In `setup`, `GET /session` and these four routes answer without credentials.
The other panel access routes answer `503` `setup_required`, including the
public sign-in and both recovery routes. The panel mirror also admits the
routes marked `duringSetup`: account-mode reads and choices, service restart,
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
characters across them, the longest text Officer stores, and an order status
list names no status twice. REST answers a value outside these limits with
`422` and code `validation`, the MCP list tools with `validation: <message>`.

## MCP tools

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
`apply_adjustment`, `set_limit`, `submit_order` and `submit_drop_copy_order`,
as a new account starts unblocked, with no barrier of its own and no balance
or P&L history. `apply_adjustment` may only lower the balance, held or incoming
of the account's own currency, and may not change the average entry price or
the realized P&L: another asset - a position included - an account without a
currency of its own, a delta above zero, an absolute above the field's current
value and an `averageEntryPrice` or `realizedPnl` other than the current one
are refused, and so is any `realizedPnl` while the position's P&L calculation
is halted, as it would restart it. An `id` starting with `trading:` is refused
too, as that namespace is reserved for the trading runtime. `delete_asset`
without `force` and a code change in
`update_asset` are refused while a rate-limit or order-size barrier references
the asset or a Spot Funds P&L bound is denominated in it, and `delete_asset`
with `force` is refused whatever the asset carries, as it would delete the
asset's dependent rows with it - balances, orders, trades, adjustments and
barriers among them. `set_limit` stores a new barrier, or one no looser than
the barrier at its address: no more orders, the same window on a broker or
asset scope and none shorter on an account or account_asset scope, every cap
and bound kept, no cap or upper bound raised and no lower bound lowered, and
the same currency. A new account-tier order-size cap must be no looser than
the asset-tier cap it overrides, a new account or group bound no looser than,
and in the currency of, the bound it takes over from, and the first order-size
barrier is refused while a rate-limit barrier is configured, as installing it
rebuilds the engine and so resets every rate window. Trading and market data
may only be narrowed: `grant_trading_access`, `create_trading_connection`,
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

Each of these tools makes the core call of one REST operation:

- `create_account`, `update_account`, `delete_account`: `POST /accounts`,
  `PUT /accounts/{code}`, `DELETE /accounts/{code}`;
- `set_account_currency`: `PUT /accounts/{code}/currency`;
- `apply_adjustment`: `POST /accounts/{code}/adjustments`;
- `create_asset`, `update_asset`, `delete_asset`: `POST /assets`,
  `PUT /assets/{code}`, `DELETE /assets/{code}`;
- `create_group`, `update_group`, `delete_group`: `POST /groups`,
  `PUT /groups/{code}`, `DELETE /groups/{code}`;
- `set_limit`: `PUT /limits/rate`, `PUT /limits/order-size` or
  `PUT /limits/spot-funds-pnl-bounds`, by its `policy` argument;
- `report_fill`: `POST /orders/{id}/execution-reports`.

All thirteen are mutating and protective and ship disabled. The path code or id
is the `account`, `asset`, `group` or `order` argument, a delete's `force` query
parameter is the `force` argument, `missingAccount` keeps its query name, and
body fields keep their REST names; `apply_adjustment` takes the whole REST body
as its `adjustment` argument, and `set_limit` takes the body of its policy's
`PUT` as flat arguments next to `policy` and refuses a field of another policy
with `validation`. `get_limits` and `get_account_state` answer every barrier
with those fields, so a rate barrier's window is `windowMs` in integer
milliseconds, as `set_limit` takes it, in place of the `window` duration string
they answered before. The `update_*` tools replace the code and title,
`update_asset` also the asset class, as `PUT` does.

Arguments reach the core as given, and the core decides every value rule, the
single-account refusal and which accounts are visible, as for REST. A tool
answers any code its REST operation documents - most often `validation`,
`not_found`, `conflict`, `accounts_not_managed`, `has_dependents`,
`currency_change_blocked`, `terminal_order`, `venue_order_managed` or the
retryable `engine_restarting` - except that the default group `-` given as
`group` is refused with `validation`, where REST answers `reserved_group`.

A tool answers the body its REST operation answers: `{"account": ...}`,
`{"asset": ...}`, `{"group": ...}`, `{"adjustment": ...}`, `{"rateLimit": ...}`,
`{"orderSizeLimit": ...}`, `{"spotFundsPnlBoundsLimit": ...}` or the
execution-report body. A delete, which REST answers without a body, answers its
own input. An adjustment the engine rejects, such as one whose resulting
balance leaves its bounds, is recorded and answered with its `rejected`
outcome, as REST answers it; one that changes nothing answers `{}`, where REST
answers 204.

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

## Trading

Trading credentials are JSON objects accepted on write; no trading route or
MCP tool returns them. A backup export carries the stored credentials according
to its `credentialForm`: sealed under a master key, plaintext without one, as with
market-data credentials. Modes are `paper` and `live`; live connection creation
is refused until the live-trading token exists. `GET /credential-storage` and
the MCP tool `get_credential_storage` report whether stored credentials are
sealed under the master key and, when they are not, whether trading connections
or market-data sources hold keys in plain text. `GET /credential-storage` also
reports `sealedAt`, when the database was sealed, and `resealedSecrets`, how
many stored secret values that sealing re-sealed - `null` and `0` for a
database never sealed; the MCP tool reports neither.
An instrument mapping whose base or quote asset Officer does not know is refused
with `asset_missing`, naming the asset; Officer never creates the asset itself.

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

Paper sends use `POST /orders/send-paper` or `send_paper_order`; live counterparts
`POST /orders/send-live` and `send_live_order` come with the live-trading token.
`acknowledged` means the venue accepted. `refused` means a definitive refusal by
the venue or Officer provably did not hand it the order, recorded cancelled with
leaves 0 and reservation released; `error` says why.
`unknown` means the call could not establish the outcome or read the stored link,
or the venue refused but cancellation is not recorded yet or could not be
confirmed; `error` says why. `GET /orders/{id}` is authoritative, and the runtime
reconciles orders that reached the send step, retrying cancellation after a
refusal.

## Single-account mode

In single-account mode every client request acts on one default account. The
mode is stored and read once, when the process starts. `GET /account-mode`
answers the stored `mode` (`single`, `multi`, or `null` until chosen, which
behaves as `multi`) and `account`, the `features` the running process enforces,
and `restartRequired`. `PUT /account-mode` stores the next mode and leaves the
running process unchanged; the change takes effect after
`POST /service/restart`. `single` takes the named account, which must exist;
with none named, the one existing account, or a new account `default` when
there is none. Several accounts without one named are refused with 422 with
constraint `required` at pointer `/account`, and a running single-account mode
accepts only its own account (409 `accounts_not_managed`: switch to `multi` and
restart first). A database reset or a backup restore that changes the stored
choice is reported by `restartRequired` too.

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
- Another account is refused with 422 with constraint `single_account_mode` at
  pointer `/account`, any account group with 422 with constraint
  `single_account_mode` at pointer `/accountGroup`, and an order of another
  account is not found.
- Creating or deleting an account, `PUT /accounts/{code}` even on the default
  account, setting an account's group, any group change, and the default-group
  currency are refused with 409 `accounts_not_managed`. The default account's
  currency, notes and block state change as usual.
- Lists and counts show the default account's rows and rows of no account;
  groups, trading grants and fee accounts of other accounts are hidden. Venue
  executions and fees still post to the account they belong to.

The mode narrows what clients see, not what they may do: backup export, backup
restore and `POST /database/reset` act on every account, and any client can
switch the mode.

The stdio MCP server is a process of its own: it reads the mode when it starts,
so it sees a change only once its host restarts it. MCP tool input schemas, over
stdio and over HTTP, are built when the process starts; in single-account mode
`account` and `missingAccount` are optional and `accountGroup` is not a
property.

## Audit source

Every control-plane action is recorded with the surface it arrived on. The
source is derived from the mount point that served the request, never from a
client-supplied header or body: `/api/v1` stamps the API source and
`/app/api/v1` the panel source, so a client selects the recorded source by
selecting the path.
