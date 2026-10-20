# Development notes

Everything a contributor needs beyond the build in the README: the developer-only
recipes, and how they differ from the default ones.

## Importable packages

A program imports Officer from the one module:

```bash
go get go.openpit.dev/officer
```

Before `1.0` the public interface is not stable: a minor version may change it,
a patch version carries fixes only. Pick a version constraint that tolerates
that.

These are the packages, by path under `go.openpit.dev/officer`; everything else
is a command or under `internal/`, and cannot be imported:

- the root - `Register` and `Config`, the default composition `pit-officer`
  runs.
- `web` - the embedded dashboard build (`Dist`).
- `httpapi` - the REST routes (`RouteConfig`) and the service lifecycle routes.
- `mcptools` - the MCP tool set (`RegisterTools`).
- `engine` - the OpenPit engine adapter (`NewOpenPitEngineBuildFunc`).
- `signing` - the Ed25519 approval signer.
- `framework/app` - the `Builder` of a composition and the `App` it builds.
- `framework/auth` - the request caller in the context, and `CallerResolver`.
- `framework/backend` - the control-plane service behind REST and MCP.
- `framework/backup` - the realm-portable backup archive format.
- `framework/businesscsv` - the business-entity CSV and ZIP format.
- `framework/domain` - the transport-agnostic value types.
- `framework/engine` - the seam between the control plane and an engine.
- `framework/health` - the diagnostic model shared by market data and trading.
- `framework/marketdata` - market-data connectors, providers, and registry.
- `framework/mcp` - the MCP tool registry and the guard around every tool call.
- `framework/mcp/catalog` - the MCP command catalogue, without the MCP SDK.
- `framework/migration` - a version-tracked database migration runner.
- `framework/node` - one engine and its store, bound to one realm.
- `framework/secret` - sealing of secret values under a master key, and
  redaction of stored credential values.
- `framework/signing` - the approval-signing seam and the approval envelope.
- `framework/store` - the database connector seam, scoped by realm.
- `framework/store/schema` - the canonical store schema every connector shares.
- `framework/trading` - trading connectors, providers, registry, and the runtime
  that sends held orders to venues and takes their executions back.
- `framework/web/httpapi` - the HTTP router, route registry, and `Authorizer`.

`checks/public-packages.txt` is meant to hold these packages and the two
commands, `cmd/pit-officer` and `examples/customhost`; `just lint-go` fails
when the paths `go list ./...` reports outside `web/node_modules/` and without
an `internal` path element drift from the list.

### Composing your own Officer

`Register` fills a builder with the default composition: the SQLite store, one
local node over the default realm running the OpenPit engine, the signer, the
control-plane service, the built-in market-data and trading providers, the REST
routes, the MCP tools, the dashboard, allow-all authorization, a caller resolver
that follows the access mode, and the browser guards - the host check over
loopback and `Config.AllowedHosts`, the cross-origin check, and the JSON-body
check. A `Set` call after it replaces that part, `SetBrowserGuards` the guards;
`AddToolRegistrar`, `RegisterMarketDataProvider`, and `RegisterTradingProvider`
add to it. `Build` then opens and migrates the store and builds the node:

```go
builder := app.NewBuilder() // go.openpit.dev/officer/framework/app
cfg := officer.Config{SQLitePath: "officer.db"}
if err := officer.Register(builder, cfg); err != nil {
    return err
}
builder.SetAuthorizer(authorizer) // decides per REST route ID or MCP tool name
if err := builder.RegisterMarketDataProvider(provider); err != nil {
    return err
}
builder.AddToolRegistrar(registerTools)

officerApp, err := builder.Build(ctx, logger, onFatal)
if err != nil {
    return err
}
defer officerApp.Close()
```

`onFatal` receives an unrecoverable persistence error; `cmd/pit-officer` logs it
and exits. It is required: `Build` rejects a nil hook.
`officerApp.RunMCPStdio` serves MCP over stdio as a given caller, and
`officerApp.BuildServeHandler` returns the HTTP handler for the dashboard, REST,
and MCP over HTTP; `cmd/pit-officer` runs both. `BuildServeHandler` fails
without all three guards, so a builder that skips `Register` sets them with
`SetBrowserGuards`; `framework/web/httpapi` builds the default ones with
`AllowedHosts`, `SameOrigin`, and `JSONBody`.

A host that composes its own `app.RouteConfig` sets its `AccessMode`, a
function the route manifest calls on every request, and the `Access` of each
extra mount, `public` or `agent`. `BuildServeHandler` fails without them, and on
an extra mount or an MCP path at or under `/api/v1` or `/app/api/v1`. The router
serves a `PanelRoutes` route marked `Public` without resolving or authorizing a
caller; `AccessMode`, `Access`, and `DuringSetup` are what the route manifest
reports, and no request is checked against them.

`BuildServeHandler` also refuses an operation mounted twice: each method and
path, parameter names ignored, may be mounted only once across the REST routes
on both prefixes, `PanelRoutes`, the route manifest, the MCP methods, the extra
mounts, and the dashboard's `GET /*`.

A host extends the route surface `Register` installs by wrapping
`builder.RouteConfigBuilder()` and setting the wrapper with
`SetRouteConfigBuilder`, as `examples/customhost/main.go` does, so the account
mode and the access mode stay served.

The account mode and the access state are part of `Register`'s factories and
private to Officer. The access state lives in Officer's SQLite store, so
`Build` fails when a host replaces the store factory and keeps the service
factory. A host that replaces the service factory runs every account, gets no
access service, and should not serve the account-mode routes `Register`'s route
config adds. `Register`'s caller resolver then answers 500 on every
authenticated route until the host installs its own with `SetCallerResolver`.
`Register`'s route config builder, if the host keeps it, serves the panel access
routes and the route manifest answering 500. A route config the host builds
from `httpapi.RouteConfig(svc, logs, sealReports)` needs a seal-report source
the host supplies, since nil panics and Officer's own is not reachable outside
`Register`, and its own `AccessMode`, without which `BuildServeHandler` fails;
its `PanelRoutes` are the host's own, since Officer's panel access routes live
in an internal package.

A host that calls the control plane itself, outside a request, puts a caller
in the context first (`auth.ContextWithCaller`): every call that reads or
changes account-owned data asks the account scope about that caller and is
refused without one, in every account mode.

`examples/customhost` is the worked example: it replaces the authorizer, adds a
market-data provider, and adds, replaces, and removes MCP tools and REST routes
on top of the default composition.

## Two build modes

Default recipes build against the published OpenPit Go module pinned in
`go.mod` - the same dependency `go get` gives a user. They do not look for a
sibling Pit checkout and do not set `OPENPIT_RUNTIME_LIBRARY_PATH`. If the
caller already exported that variable, the binding still honors it in the normal
Go way.

Recipes ending in `-dev` build against a local Pit checkout instead. They need a
Rust toolchain, because they build the native runtime from source. They:

1. build the native runtime from that checkout
   (`cargo build -p openpit-ffi --locked`, plus `--release` outside debug mode);
2. resolve the Go binding from the same checkout through a `go.work` written to
   a temporary directory and passed via `GOWORK`, so `go.mod` and `go.sum` are
   left untouched;
3. point `OPENPIT_RUNTIME_LIBRARY_PATH` at the freshly built library under the
   checkout's `target/`.

The checkout path defaults to `../pit`, relative to this module root. Pass a
path as the recipe argument to override it.

```bash
just build-dev                 # build against ../pit
just build-dev /path/to/pit    # build against an explicit checkout
just run-serve-dev             # build against ../pit, then serve
just run-mcp-dev               # build against ../pit, then stdio MCP
just check-dev                 # the full gate against ../pit
just dylib-dev                 # only build the native runtime
```

Every recipe that links against the native runtime - the build, vet, lint-go,
test, check, and run recipes - has a `-dev` twin, and the `-dev` argument is
always the checkout path. The rest have none, because none of them links against
the runtime: `tidy` and `fmt-go` on the Go side, and `build-js`, `build-js-lib`,
`fmt-js`, `lint-js`, `test-js`, `check-js`, `check-js-dry`, and `check-semgrep`
on the frontend and Semgrep side.

## Debug and release variants

Recipes come in `-debug` and `-release` variants; the bare name is the release
one. `-debug` adds `-gcflags=all=-N -l` to the Go build and, in dev mode, builds
the native runtime without `--release`. The two profiles live in different
`target/` directories, and each `-dev` recipe builds the runtime for its own
profile before it runs.

```bash
just build-debug
just check-debug
just build-debug-dev /path/to/pit
```

## Building without Just

```bash
cd web && npm ci && npm run build && cd ..
CGO_ENABLED=1 go build -o pit-officer ./cmd/pit-officer
```

Build the SPA first. `//go:embed dist` takes whatever is in that directory
at build time, and a fresh checkout carries only the placeholder file there; a
binary built over the placeholder has no `index.html` to serve, and `serve`
fails when it builds the SPA handler at startup.

## Frontend

`just frontend-ci-install` installs from the committed lockfile (`npm ci`);
`just frontend-install` runs `npm install` and is what the binary build recipes
use. `just build-js` builds the SPA into `web/dist`, and `just build-js-lib`
builds the publishable framework library into `web/lib`. Only
`web/dist/embed-placeholder.txt` is committed; the rest of `web/dist` is build
output, and `//go:embed dist` takes whatever is in that directory when the
Go build runs.

## Gates

`just check` is the whole gate. Narrower ones exist for a faster loop:
`just check-go`, `just check-js`, `just lint-go`, `just lint-js`,
`just check-semgrep`, `just test-go`, `just test-js`. The `-dry` variants lint
and test without rebuilding artifacts.

`just build-go` only compiles the Go packages and produces no binary; use it as
a compile check.

Semgrep runs from a virtualenv in `.venv`, created and populated by
`just install-semgrep` from `requirements.txt` on the first run and skipped
afterwards while the installed version matches the pin.

CI does not call `just check` directly. It runs the aggregates in
`pipeline.just` (`ci-go`, `ci-js`), which import this justfile; tool versions
come from `.github/ci-versions.env`, which the justfile loads as its dotenv
file.

## Container image

```bash
just docker-build                 # tags pit-officer:local
just docker-build myrepo/pit-officer:dev
```

The image is built from the published OpenPit module, and the Docker build
overwrites `web/dist` with compiled assets before the Go build.

The build needs BuildKit, the default builder in current Docker, because the SPA
stage runs on the build platform via `$BUILDPLATFORM`.

A `vX.Y.Z` tag on a commit already on `main` publishes the image for
`linux/amd64` and `linux/arm64` to `ghcr.io/openpitkit/officer` as `X.Y.Z`.
`X.Y` moves to it only when it is the highest stable `X.Y.*` version on `main`,
and `latest` moves only when it is the highest stable version on `main` overall.
A tag on any other commit fails the workflow; a pre-release tag such as
`v0.1.0-rc.1` gets only its own version. The publishing job runs in the
protected `release` environment and starts only once a required reviewer
approves it. The workflow is `.github/workflows/release-image.yml`.

`just release-dry` tries the build and publication part of that path before a
release: it force-pushes `HEAD` to the `release-dry-run` branch and dispatches
the same workflow as a dry run, the only kind of dispatch it accepts. It runs
outside the `release` environment, which admits only `v*` tags, so it waits for
no approval. The dry run publishes both platforms to the same package under
its own `dry-run-<sha>-<run>.<attempt>` tag and never moves `X.Y.Z`, `X.Y` or
`latest`. It then pulls each platform back, starts `serve`, and requires the
container to become healthy and `/healthz` to answer 200. Last, it deletes the
older dry-run images, so the package holds only the newest one. The `main`
check and the ranking of `X.Y` and `latest` run only on a real version tag.

`just docker-start-check <image> <platform>` runs the same start check on a
local image, for example `just docker-start-check pit-officer:local
linux/arm64`.

## Seeding

`just seed` fills a running instance with demo accounts, balances, orders, and
trades through its API.
