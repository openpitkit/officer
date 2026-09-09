# Configuration reference

Pit Officer is configured through environment variables and command-line flags.
Precedence is flags, then the environment, then the built-in defaults. An empty
environment value is treated as unset for `PIT_OFFICER_HTTP_ADDR`,
`PIT_OFFICER_SQLITE_PATH`, and `OPENPIT_RUNTIME_LIBRARY_PATH`, so it does not
shadow the default; for the two master-key variables an empty value is
configured but invalid, not absent.

The run mode is not configured here - it comes from the subcommand.

| Environment variable | Flag | Default | Applies to |
| --- | --- | --- | --- |
| `PIT_OFFICER_HTTP_ADDR` | `-http-addr` | `127.0.0.1:0` | `serve`; also read by `healthcheck` as the fallback address when no runtime-state file is found |
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
  is not 200.

## Authorization

Pit Officer authorizes every request that reaches it: there is no authentication
in front of the panel, the API, or MCP-over-HTTP. Loopback is the boundary, and
binding a non-loopback address is a deliberate operator decision.

## HTTP address

The default binds loopback with an OS-assigned free port, so the address is not
known until the listener is up. Use `pit-officer dashboard` to discover it. The
container image overrides this to `0.0.0.0:8787` so a fixed, mapped port can be
reached from the host.

## SQLite path

The on-disk path of the database. The container image sets it to
`/data/pit-officer.db` and maps `/data` to a named volume. The path also decides
where the runtime-state file goes - see below.

## Runtime library path

An explicit path to a pre-extracted native OpenPit runtime library. When set,
the Go binding skips its own extraction step. The container image leaves it
unset.

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
recovers it; the operator supplies it whole.

The key has no command-line flag of its own, deliberately: process arguments are
visible to other local processes. Only the _path_ to a key file is a flag.

If both sources are configured they must carry the same key, and a configured
source that cannot be read or parsed is a startup error. Officer refuses to
start rather than silently running unencrypted.

Startup resolves to one of four states:

- **Unsealed database, no key** - secrets are stored as plaintext. This is the
  default.
- **Unsealed database, key supplied** - the database is sealed, then vacuumed.
  For a database that already existed, Officer logs that previously stored
  secrets must be considered exposed and should be reissued: they were on disk
  in the clear, and sealing cannot undo that.
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
