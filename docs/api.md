# API notes

What a client needs beyond the endpoint reference: where the surface is mounted,
what it answers with, and the two error shapes.

## Mount points

REST is mounted at `/api/v1`. A mirror at `/app/api/v1` is reserved for the
operator panel. The two carry the same routes except the route manifest, which
is published only on `/api/v1`; otherwise they differ only in the source stamped
on the audit record. Outside them, MCP over HTTP is mounted at `/mcp`, liveness
at `/healthz`, the OpenAPI document at `/api/openapi.yaml`, its docs page at
`/docs`, and the Swagger UI files under `/docs/` (`GET` only). The dashboard
answers `GET` and `HEAD` on every other path; any other standard method there
answers `404` `not_found`.

## Discovering the surface

The OpenAPI 3.1 document describes the `/api/v1` surface. It is served at
`/api/openapi.yaml` and browsable at `/docs`. It is hand-written and embedded in
the binary, and what lives outside `/api/v1` - liveness, the document itself,
the docs page, MCP over HTTP - is not in it.

`/docs` serves Swagger UI from the binary, pinned to the vendored
`swagger-ui-dist` release that `just vendor-swagger-ui` refreshes, under a
content security policy that keeps its scripts, styles and requests on
Officer's own origin. The document at `/api/openapi.yaml` is served from the
binary and needs nothing.

The route manifest, `GET /api/v1/route-manifest.json`, lists every mounted
route - the REST routes on `/api/v1` and `/app/api/v1`, MCP over HTTP,
liveness, the document, the docs page and its files, and the dashboard - with
its full path and the access it requires (`public`, `api`, `agent`, or
`panel`), and the service's `accessMode`. Officer reports `open`: no route asks
for credentials, and the browser checks under Errors still apply. The REST
entries are filtered for the caller; the others are listed for every caller.

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

Three browser checks refuse a request before it reaches a handler, in the
second shape:

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
- `report_fill`: `POST /orders/{id}/execution-reports`.

All twelve are mutating and protective and ship disabled. The path code or id
is the `account`, `asset`, `group` or `order` argument, a delete's `force` query
parameter is the `force` argument, `missingAccount` keeps its query name, and
body fields keep their REST names; `apply_adjustment` takes the whole REST body
as its `adjustment` argument. The `update_*` tools replace the code and title,
`update_asset` also the asset class, as `PUT` does.

Arguments reach the core as given, and the core decides every value rule, the
single-account refusal and which accounts are visible, as for REST. A tool
answers any code its REST operation documents - most often `validation`,
`not_found`, `conflict`, `accounts_not_managed`, `has_dependents`,
`currency_change_blocked`, `terminal_order`, `venue_order_managed` or the
retryable `engine_restarting` - except that the default group `-` given as
`group` is refused with `validation`, where REST answers `reserved_group`.

A tool answers the body its REST operation answers: `{"account": ...}`,
`{"asset": ...}`, `{"group": ...}`, `{"adjustment": ...}` or the
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
or market-data sources hold keys in plain text.
An instrument mapping whose base or quote asset Officer does not know is refused
with `asset_missing`, naming the asset; Officer never creates the asset itself.

A venue-linked order is managed by its venue connection. Manual execution
reports, confirm and cancel refuse it with `venue_order_managed`. Trading MCP
tools follow the catalog rule above: mutating tools ship disabled.

Access to a connection is granted to an Officer account or an account group:
`PUT` and `DELETE /trading/connections/{id}/access` take `account` or
`accountGroup`, as do the MCP tools `grant_trading_access` and
`revoke_trading_access`. A member account sends through its group's grant; the
account's own grant on the same connection wins. On providers with unlinked fees
a connection without a fee account books them to the one account that can send
through it; when none or several can, each fee is skipped with a
`venue_fee_skipped` audit. A group grant, or a grant to a second account, needs
the fee account there and is refused with `fee_account_required` without one.

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
