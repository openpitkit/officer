# Configuration reference

Pit Officer is configured through environment variables and command-line flags.
Precedence is flags, then the environment, then the built-in defaults - except
for the runtime library path, whose flag must match the environment (see
below). An empty
environment value is treated as unset for `PIT_OFFICER_HTTP_ADDR`,
`PIT_OFFICER_SQLITE_PATH`, `OPENPIT_RUNTIME_LIBRARY_PATH`, and
`PIT_OFFICER_ALLOWED_HOSTS`, so it does not shadow the default; for the two
master-key variables an empty value is configured but invalid, not absent.

To install and start Officer from the published Docker image, see the
[quickstart](quickstart.md).

The run mode is not configured here - it comes from the subcommand.

| Environment variable | Flag | Default | Applies to |
| --- | --- | --- | --- |
| `PIT_OFFICER_HTTP_ADDR` | `-http-addr` | `127.0.0.1:0` | `serve`; also read by `healthcheck` as the fallback address when no runtime-state file is found |
| `PIT_OFFICER_ALLOWED_HOSTS` | - | _(empty)_ | `serve`; every subcommand refuses to start on an invalid value |
| `PIT_OFFICER_SQLITE_PATH` | `-sqlite-path` | `pit-officer.db` | all subcommands; the runtime-state file path derives from it, and from it alone |
| `OPENPIT_RUNTIME_LIBRARY_PATH` | `-runtime-library-path` | _(empty)_ | `mcp`, `serve` |
| `PIT_OFFICER_OPEN_BROWSER` | `-open-browser` | `true` | `serve` |
| `PIT_OFFICER_MASTER_KEY` | - | _(empty)_ | `mcp`, `serve` |
| `PIT_OFFICER_MASTER_KEY_FILE` | `-master-key-file` | _(empty)_ | `mcp`, `serve` |

## Subcommands

`pit-officer` carries four:

- `serve` - the always-on service: dashboard, REST API, and MCP over streamable
  HTTP. It binds `127.0.0.1:0` - loopback, OS-assigned free port.
- `mcp` - a local stdio
  [MCP](https://modelcontextprotocol.io/docs/getting-started/intro) server,
  launched on demand by an MCP client. No network listener is opened.
- `dashboard` - print a running instance's URLs and open it in the browser; it
  reads the address `serve` published next to the database, so both need the
  same database path.
- `healthcheck` - probe a running instance's `/healthz` and exit non-zero if it
  is not 200. The container image runs it as its Docker `HEALTHCHECK`.

## Authorization

Pit Officer has three access states. The state is kept in the database, not
in the environment: a new database starts in `setup`, and so does the one
`POST /database/reset` recreates. First-run setup completes in the panel with
`POST /app/api/v1/setup/complete`, choosing `open` or `token`. The operator can
then switch between those modes with `PUT /app/api/v1/access/mode`, with no
restart; only a database reset returns to `setup`. A backup carries no access
state, so a restore keeps the state, the password, the tokens, and the sessions.

- **`setup`** - `/api/v1`, including the route manifest, and `/mcp` answer
  `503` `setup_required`. The panel's API on `/app/api/v1` admits only routes
  marked `duringSetup`, without credentials; the rest answer the same error.
- **`open`** - no surface asks for credentials: Officer authorizes every
  request that passes the browser checks below.
- **`token`** - each HTTP surface admits only its own credential: the REST API
  on `/api/v1` the API token, `pit_api_...`; MCP over HTTP on `/mcp` the agent
  token, `pit_mcp_...`; the panel's API on `/app/api/v1` a session that
  signing in with the panel password (`POST /app/api/v1/session`) opens. A
  token is sent as `Authorization: Bearer <token>`. The other surface's token
  is refused with `403` `wrong_token`, a missing or unknown credential with
  `401` `unauthorized`.

In `setup`, `/healthz`, `/docs` and its files, `/api/openapi.yaml`, and the
panel's static files answer. `GET /app/api/v1/session`, the four setup routes,
and the panel mirror's routes marked `duringSetup` answer; public sign-in and
recovery routes refuse with `503` `setup_required`. The stdio `mcp` subcommand
works in `setup` as in the other modes and asks for no credentials.

Completing setup in `token` or switching to it sets the panel password,
8 to 256 characters, and issues both tokens.
`GET /app/api/v1/access/tokens/{kind}` answers a token and
`POST /app/api/v1/access/tokens/{kind}/regenerate` reissues it; a reissued token
replaces the old one at once. Switching to `open` deletes the password, both
tokens, and every session. The bodies of these routes are in
[Panel access](api.md#panel-access) in the API notes.

In `token` mode the server refuses a protected `/app/api/v1` request without a
live panel session with `401` `unauthorized`. The public session and recovery
routes need no session.

A forgotten panel password is reset with a one-time recovery code:
`POST /app/api/v1/session/recovery` asks for one, and
`POST /app/api/v1/session/recovery/reset` sets a new password with it. Officer
prints the code to its standard error - under Docker the container log
(`docker logs pit-officer` for the container the [quickstart](quickstart.md)
starts), otherwise the terminal running `pit-officer serve`:

```text
Pit Officer panel recovery code: XXXX-XXXX-XXXX (single use, valid 15 minutes)
```

The code is written there and nowhere else: neither the service log the panel
shows, nor the audit record, nor any response carries it. It is valid for 15
minutes and one use; five wrong attempts, a password change, or a restart void
it. While a code is valid, a new request issues no other, and a new code is
issued at most once a minute. A reset with the code ends every session and
leaves both tokens as they are.

In every state each HTTP request first passes three checks, made before
credentials are read and before the request reaches a handler:

- the `Host` header must name an allowed host (see below), or the request is
  refused with `403` `host_not_allowed`;
- a request other than `GET`, `HEAD`, or `OPTIONS` that a browser sent from
  another origin is refused with `403` `origin_not_allowed`; MCP over HTTP
  checks the origin on every method, `GET` included, since a `GET` there opens
  an event stream and an MCP session; a request carrying neither
  `Sec-Fetch-Site` nor `Origin`, as a program sends it, passes;
- a request other than `GET`, `HEAD`, or `OPTIONS` that carries a body not
  declared as `application/json` is refused with `415`
  `unsupported_media_type`, before the body is read.

The checks refuse a state-changing request that a page from another site, open
in the operator's browser, sends as a form or a script, and a request to a
domain re-bound to Officer's address. Loopback is the network boundary.
Binding a non-loopback address is a deliberate operator decision, and clients
that reach it by a name or address other than loopback need that name in
`PIT_OFFICER_ALLOWED_HOSTS`.

## HTTP address

The default binds loopback with an OS-assigned free port, so the address is not
known until the listener is up. Use `pit-officer dashboard` to discover it. The
container image overrides this to `0.0.0.0:8787` so a fixed, mapped port can be
reached from the host.

## Allowed hosts

`serve` answers a request only when its `Host` names an allowed host:
`localhost`, `127.0.0.1`, and `::1` always, plus the names in
`PIT_OFFICER_ALLOWED_HOSTS`. Add a name when clients reach Officer by another
one - a container service name, `host.docker.internal`, or the address of a
widened bind. A request to any other name, the dashboard's included, answers
`403` with code `host_not_allowed`.

The value is a comma-separated list, each name trimmed of surrounding
whitespace. A name is an IP address - IPv6 without brackets or zone - or an
ASCII DNS name: labels of letters, digits, `-`, and `_` joined by single dots,
the last label not a number. A name with a port, a wildcard, or brackets, an
empty entry, and any other name fail the start of every subcommand. Unset or
blank, the variable adds no name. It has no flag.

A name matches the request's host exactly and case-insensitively, an IP
address in its canonical form; the port is not checked.

## SQLite path

The on-disk path of the database. The container image sets it to
`/data/pit-officer.db` and maps `/data` to a named volume. The path also decides
where the runtime-state file goes - see below.

## Runtime library path

The OpenPit SDK loads its native runtime once, at process start: the library
`OPENPIT_RUNTIME_LIBRARY_PATH` names, or the runtime embedded in the SDK when the
variable is unset. Nothing later in the process can change it, so set the
variable in the environment that starts `pit-officer`.

`-runtime-library-path` does not choose the runtime. When it is set, `mcp` and
`serve` compare it with the library the SDK reports it loaded - the flag's
surrounding whitespace trimmed and its path cleaned, so `/a/b/` and `/a/b`
match - and refuse to start, naming both, when they differ. The container image
leaves both unset.

With `OPENPIT_RUNTIME_LIBRARY_PATH` unset, the SDK extracts its embedded runtime
into the OS user cache directory, or into `OPENPIT_RUNTIME_CACHE_DIR` when that
is set. The container image sets it to `/tmp/openpit-runtime` because its user
has no home directory. A container with a read-only root filesystem needs `/tmp`
writable and executable, as `docker-compose.yml` mounts it.

## Browser launch

`serve` opens the dashboard in the default browser once the listener is up.
Opening is best-effort and runs off the startup path: a headless host or a
missing opener is logged as a warning and never fails `serve`.

Set `PIT_OFFICER_OPEN_BROWSER` to `false`, `0`, `no`, or `off` (case-insensitive,
surrounding whitespace ignored) to suppress it. Any other value, including an
empty one, leaves the default in place. The `-open-browser` flag overrides the
environment in both directions - `-open-browser=false` suppresses, a bare
`-open-browser` forces it on.

`docker-compose.yml` sets `PIT_OFFICER_OPEN_BROWSER: "false"` because a
container has no browser to open. When `serve` restarts itself on an operator's
request it re-executes with `-open-browser=false`, so a restart does not open a
second tab.

`pit-officer dashboard` opens the browser too, and has its own `-no-open` flag
for printing the URLs without opening anything. That flag belongs to
`dashboard`; it is not accepted by `serve`.

## Master key

`PIT_OFFICER_MASTER_KEY` and `PIT_OFFICER_MASTER_KEY_FILE` supply the key that
seals stored secrets at rest. The value is exactly 32 bytes in standard base64,
in the variable itself or in the named file. Officer never generates, stores, or
recovers it; the operator supplies it whole. The access tokens and the panel
password hash (see Authorization) are stored secrets too, sealed like the
others.

The key has no command-line flag of its own, deliberately: process arguments are
visible to other local processes. Only the _path_ to a key file is a flag.

If both sources are configured they must carry the same key, and a configured
source that cannot be read or parsed is a startup error. Officer refuses to
start rather than silently running unencrypted.

An approval signing private key is a stored secret like the others: sealed
under the master key when one is configured, and in the clear without one.
Approval signing is off on a fresh Officer. To turn it on, generate or import a
signing key on the Signing Keys screen and switch on "Require signature on
approval and attestation envelopes".

Startup resolves to one of six states:

- **Unsealed database, no key** - trading and market-data credentials, the
  access tokens and an approval signing private key are stored as plaintext.
  This is the default.
- **Unsealed database, key supplied** - the database is sealed, then vacuumed.
  When sealing re-sealed at least one stored secret value, an empty credential
  not counted, Officer logs that previously stored secrets must be considered
  exposed and should be reissued: they were on disk in the clear, and sealing
  cannot undo that.
- **Sealed database, matching key** - Officer starts with stored secrets
  sealed. If an earlier sealing never recorded its vacuum, the database is
  vacuumed first, and Officer logs the same warning when that sealing
  re-sealed at least one stored secret value.
- **Sealed database without a seal report, matching key** - Officer refuses to
  start and asks for the database to be recreated. Sealing records when it ran
  and how many values it re-sealed; only an earlier build could have sealed a
  database without that record.
- **Sealed database, no key** - Officer refuses to start.
- **Sealed database, wrong key** - Officer refuses to start.

> **The key is not recoverable.** There is no escrow, no recovery code, and no
> way to open a sealed database without the exact key that sealed it. Lose the
> key and Officer will never open that database again - not only the sealed
> secrets, the whole store. Keep a copy of the key somewhere other than the
> machine holding the database, and be sure you can restore it before you seal
> anything.

## Runtime-state file

`serve` binds its listener before its address is knowable by anyone else, so it
publishes what it bound: a file named `officer-runtime.json`, written next to
the SQLite database, holding the bound address, the client-reachable URL, the
process id, and an RFC 3339 start timestamp. It is written atomically - a temp
file in the same directory, renamed into place - so a reader never sees a
partial file, and it is removed when `serve` shuts down.

`dashboard` and `healthcheck` derive the same path from the same configuration
and read it to find the live instance. They open no store and no engine.

The path is derived from the database path, not from the address, so the three
commands agree only when they share configuration. Run them from the same
working directory, or give all three the same `PIT_OFFICER_SQLITE_PATH` /
`-sqlite-path`. `dashboard` reports that `serve` does not appear to be running
when the file is absent; `healthcheck` falls back to the configured address.
