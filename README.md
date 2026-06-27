# Pit Officer

Pit Officer is an open-source control plane that sits over the
embeddable [OpenPit](https://openpit.dev) pre-trade risk engine. It exposes the
engine to operators and to AI agents without putting any risk logic of its own
in front of the engine: Pit Officer hydrates, observes, and operates the
engine; the engine alone evaluates orders.

## Surfaces

### REST API

The `serve` mode exposes a REST API under `/api/v1`. Interactive documentation
is available at `/docs` (Swagger UI) once the service is running. The
machine-readable OpenAPI 3 spec is at `/api/openapi.yaml`.

All responses use `application/json`; errors return
`{"error":{"code":"...","message":""}}`.

### MCP tools

The MCP surface is read-only and carries no secrets. Tool names:

<!-- markdownlint-disable MD013 -->

| Tool | Parameters | Returns |
| --- | --- | --- |
| `health` | - | Liveness state |
| `get_account_state` | `account` (string) | Account detail and its limits |
| `get_limits` | `account` (string, optional) | All limits or per-account limits |
| `get_audit` | `category` (control \| trading \| all, default control), `account` (string, optional), `limit` (int, optional, default 50, cap 500) | Audit entries |

<!-- markdownlint-enable MD013 -->

JSON shapes are identical to the REST DTOs.

### Dashboard pages

The operator SPA (`serve` mode) provides four pages:

- **Dashboard** - engine status and node health at a glance.
- **Accounts** - list, create, block, and unblock accounts.
- **Limits** - browse and edit risk limit barriers across all policies.
- **Audit** - append-only audit trail of all control-plane actions.

## Extending Pit Officer

The open app is itself a consumer of the public framework packages under
`go.openpit.dev/officer/framework/...`; `go.openpit.dev/officer/openapp.Register`
is the open composition entry point. A separate distribution can start from the
same framework seams and add, replace, hide, or remove surfaces by composition.
The worked proof is `examples/closedref`, which imports framework packages and
the public `openapp` entry point without importing `go.openpit.dev/officer/internal`.

- Domain types live in `go.openpit.dev/officer/framework/domain`. Caller/source
  context lives in `go.openpit.dev/officer/framework/auth` through
  `ContextWithCaller` and `CallerFromContext`.
- Persistent state is the `store.Store` / `store.RealmStore` contract from
  `go.openpit.dev/officer/framework/store`. Versioned schema application is
  `migration.Apply` over a `migration.MigrationSource` from
  `go.openpit.dev/officer/framework/migration`.
- Engine wiring is split between `engine.Engine` and `engine.BuildFunc` in
  `go.openpit.dev/officer/framework/engine`, and `node.Node` /
  `node.NodeRouter` in `go.openpit.dev/officer/framework/node`.
- Market-data extensions use `marketdata.Registry.Register` and
  `marketdata.Registry.Unregister` from
  `go.openpit.dev/officer/framework/marketdata`. Providers are keyed by stable
  `Provider.Type`; connector factories emit `QuoteUpdate` values into the
  injectable `marketdata.Sink`.
- Approval-token signing depends only on `signing.Service` from
  `go.openpit.dev/officer/framework/signing`; concrete key management belongs
  to the consuming distribution.
- REST routes use `httpapi.RouteRegistry.Register` and
  `httpapi.RouteRegistry.Unregister` from
  `go.openpit.dev/officer/framework/web/httpapi`. Routes are keyed by stable
  `Route.ID`. Re-registering an id replaces the entry in place, unregistering
  removes it structurally, and the `Authorizer` middleware hides a registered
  route by denying its permission.
- MCP tools use `mcp.ToolRegistry.Register`, `mcp.ToolRegistry.Unregister`, and
  `mcp.ToolRegistry.Catalog` from `go.openpit.dev/officer/framework/mcp`.
  Descriptors are keyed by stable tool name. Re-registering replaces,
  unregistering removes both handler and catalogue projection, and
  `mcp.Guard` applies the `Authorizer` plus per-command enabled state. The SDK
  free catalogue view is `go.openpit.dev/officer/framework/mcp/catalog`.

Every registry uses stable ids deliberately: add with a new id, replace by
registering the same id, remove with the explicit unregister API, and hide by
leaving the entry registered while the `Authorizer` denies it. The reference
composition demonstrates a private route, MCP tool, market-data provider, custom
authorizer, replacement, hiding, and removal without forking or patching the
open app.

### Deferred extension work

The transport-agnostic node and engine seam is ready for a remote or RPC-backed
engine node, but the only shipped implementation today is the in-process cgo
node used by the open binary.

The market-data manager accepts quotes through the injectable `marketdata.Sink`,
so a distribution can later place a distribution mesh in front of the sink
without changing the framework or the open connectors.

The open repository ships an allow-all `Authorizer`. A consuming distribution is
expected to replace it with real user, company, and entitlement logic through
the same HTTP and MCP authorization seam.

## Run modes

Pit Officer ships as a single binary, `pit-officer`, with four subcommands:

- `pit-officer mcp` - a local stdio [Model Context Protocol (MCP)](https://modelcontextprotocol.io/docs/getting-started/intro)
  server. Intended to be launched on demand by an MCP client (an editor, an
  agent runtime) over standard input/output. No network listener is opened.
- `pit-officer serve` - an always-on service that exposes the same MCP surface
  over streamable HTTP and serves the operator dashboard (an embedded
  single-page app under `web/dist`). It binds a free loopback port by default
  (`127.0.0.1:0`, OS-assigned); binding to a non-loopback address is an
  explicit, deliberate operator decision.
- `pit-officer dashboard` - print a running instance's dashboard URL and open
  it in the browser. It opens no engine or store; it locates the instance via
  the runtime-state file `serve` writes next to the database, so it must be run
  with the same configuration (working directory / `PIT_OFFICER_SQLITE_PATH`).
- `pit-officer healthcheck` - probe a running instance's `/healthz` and exit
  non-zero if it is not 200. It also reads the runtime-state file, so it too
  must share the `serve` configuration.

Because `serve` binds a free port by default, the bound port is not known until
it is listening. `serve` publishes its real address to a runtime-state file
(`officer-runtime.json`) next to the database; `dashboard` and `healthcheck`
read that file to find the live URL.

## Configuration

Pit Officer is configured through environment variables (or command-line flags that
override them). Flags take precedence over the environment; the environment
takes precedence over built-in defaults.

<!-- markdownlint-disable MD013 -->

| Environment variable | Flag | Default | Description |
| --- | --- | --- | --- |
| `PIT_OFFICER_HTTP_ADDR` | `-http-addr` | `127.0.0.1:0` | HTTP listen address used in `serve` mode. The default binds to loopback with an OS-assigned free port; use `pit-officer dashboard` to discover the URL. The container image overrides this to `0.0.0.0:8787` so a fixed, mapped port can be reached. |
| `PIT_OFFICER_SQLITE_PATH` | `-sqlite-path` | `pit-officer.db` | On-disk path of the SQLite database. The container image sets this to `/data/pit-officer.db` and maps `/data` to a named volume. |
| `OPENPIT_RUNTIME_LIBRARY_PATH` | `-runtime-library-path` | _(empty)_ | Path to a pre-extracted native OpenPit runtime library. When set, the binding skips its own extraction step. The stable container image leaves this unset. |

<!-- markdownlint-enable MD013 -->

### Loopback default vs. container networking

Out of the box, `pit-officer serve` binds to `127.0.0.1:0` - loopback only, with
an OS-assigned free port. This is deliberate: the service must not be reachable
from outside the local host without an explicit, operator-visible override, and
the free port avoids collisions on a developer host. Run `pit-officer dashboard`
to print the resolved URL and open it.

Inside a container a free port cannot be mapped, so the Dockerfile and
`docker-compose.yml` both set `PIT_OFFICER_HTTP_ADDR=0.0.0.0:8787` - a fixed port
bound on all interfaces - and Docker's port-mapping (`-p 127.0.0.1:8787:8787`)
controls host-side exposure. Production deployments should place a
TLS-terminating reverse proxy in front rather than exposing the port directly.

## Build

### Prerequisites

The OpenPit Go binding requires cgo. Stable builds use the published
`go.openpit.dev/openpit v0.5.0` module pinned in `go.mod`, the same dependency
users get with `go get`.

- cgo enabled (`CGO_ENABLED=1`) and a working C toolchain.

By default, the `just` recipes do not use a sibling Pit checkout and do not set
`OPENPIT_RUNTIME_LIBRARY_PATH`. If that environment variable is already set by
the caller, the binding still honors it in the normal Go way. The SPA must be
installed and built before `go build` so the `//go:embed` directive finds the
assets.

With [Just](https://just.systems/):

```bash
go mod tidy   # update go.sum after editing go.mod
just check    # format, lint, build, and test
just build-go # build all Go packages without rebuilding the SPA
just build    # build the SPA, then the pit-officer binary
```

Local OpenPit developer mode is explicit. These recipes build the native runtime
from a local Pit checkout, resolve the Go binding from that same checkout via a
temporary `go.work`, and leave the stable `go.mod` / `go.sum` unchanged. The
default checkout path is `../pit` from this `officer/` directory; pass a path to
override it.

```bash
just build-go-dev           # uses ../pit
just build-dev              # uses ../pit
just build-dev /path/to/pit # uses an explicit Pit checkout
```

Manual stable build:

```bash
CGO_ENABLED=1 go mod tidy

# Install and build the SPA, then build the binary:
cd web && npm install && npm run build && cd ..
CGO_ENABLED=1 go build -o pit-officer ./cmd/pit-officer
```

Once `web/package-lock.json` is committed, replace `npm install` with `npm ci`
for reproducible, lockfile-pinned installs.

## Run

`serve` binds a free loopback port by default, so the URL is not known ahead of
time. Start `serve`, then run `dashboard` to print and open it. `dashboard` and
`healthcheck` locate the running instance through the runtime-state file `serve`
writes next to the database, so they must use the same configuration (working
directory / `PIT_OFFICER_SQLITE_PATH`) as the `serve` process.

With [Just](https://just.systems/):

```bash
just run-mcp     # local stdio MCP server (rebuilds first)
just run-serve   # always-on dashboard + MCP-over-HTTP (rebuilds first)
just dashboard   # print and open the running serve URL (no rebuild)
```

Local OpenPit developer mode:

```bash
just run-mcp-dev             # uses ../pit
just run-serve-dev           # uses ../pit
just run-serve-dev /path/to/pit
```

Manual:

```bash
./pit-officer mcp           # local stdio MCP server
./pit-officer serve         # always-on dashboard + MCP-over-HTTP
./pit-officer dashboard     # print and open the running serve URL
./pit-officer healthcheck   # probe /healthz; exit non-zero if unhealthy
```

## License

Apache License 2.0. See [LICENSE](LICENSE).
