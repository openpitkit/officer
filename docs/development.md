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
- `framework/web/httpapi` - the HTTP router, surfaces, route registry, and
  `Authorizer`.

`checks/public-packages.txt` is meant to hold these packages and the two
commands, `cmd/pit-officer` and `examples/customhost`; `just lint-go` fails
when the paths `go list ./...` reports outside `web/node_modules/` and without
an `internal` path element drift from the list.

### Composing your own Officer

`Register` fills a builder with the default composition: the SQLite store, one
local node over the default realm running the OpenPit engine, the signer, the
control-plane service, the built-in market-data and trading providers, the REST
routes, the MCP tools, the dashboard, Officer's authorizer, a caller resolver
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
installedAuthorizer := builder.Authorizer()
if installedAuthorizer == nil {
    return errors.New("officer.Register installed no authorizer")
}
authorizer := newDenyOneAuthorizer(hiddenBaseRouteID, hiddenToolID, installedAuthorizer)
builder.SetAuthorizer(authorizer)
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

The authorizer wrapper above is the one in `examples/customhost`: it refuses
its chosen REST route ID and MCP tool name and delegates every other decision
to `builder.Authorizer()`, the authorizer `Register` installed. `Register`'s
caller resolver refuses REST, MCP over HTTP and non-`DuringSetup` panel routes
in `setup` before any authorizer runs; it alone closes MCP over HTTP. Replacing
the authorizer keeps this resolver guard. The installed authorizer repeats the
refusal for REST and panel routes, including with a host resolver, and passes
MCP for stdio. A host that calls
`SetCallerResolver` must refuse REST and MCP over HTTP in `setup` itself and
keep `builder.Authorizer()` in its authorizer chain; the refusals
`builder.Authorizer()` returns in `setup` carry `domain.ErrSetupRequired`'s
message, which names the panel instead of an address.

For a surface caller (the panel, the REST API, MCP), the authorizer set with
`SetAuthorizer` is also asked before a forced deletion of an account, group,
asset or asset class and before a forced execution report, using
`force:<audit action>` identifiers from the `backend.Force*` constants.
Officer's own processing (`domain.SourceSystem`) is not asked. A refusal
returns the authorizer's error (`domain.ErrForbidden` answers HTTP 403 with
code `forbidden`) and is recorded in the audit log. A host replacing the
service factory passes `backend.WithForceAuthorizer(builder.Authorizer())`
to `backend.New` itself, reading the authorizer inside the factory at `Build`
so a wrapper set later is the one asked. Without the option, every forced
operation is allowed.

`onFatal` receives an unrecoverable persistence error; `cmd/pit-officer` logs it
and exits. It is required: `Build` rejects a nil hook.

`officerApp.RunMCPStdio` serves MCP over stdio as a given caller, and
`officerApp.BuildServeHandler` returns the HTTP handler for the dashboard, REST,
and MCP over HTTP; `cmd/pit-officer` runs both. `BuildServeHandler` fails
without all three guards, so a builder that skips `Register` sets them with
`SetBrowserGuards`; `framework/web/httpapi` builds the default ones with
`AllowedHosts`, `SameOrigin`, and `JSONBody`.

`BuildOver` composes over a ready node, signer, and running market-data and
trading runtimes supplied by the host through a factory. Like `Build`, it
requires an authorizer, caller resolver, service factory, and route config
builder; unlike `Build`, it takes no fatal hook; its factory receives the
builder's market-data and trading registries.
It refuses a builder with a store factory, node builder, node hook, or signing
factory, including one filled by `Register`. The factory owns its parts until
it returns successfully and releases them itself on error; after success,
`BuildOver` releases them if composition fails, joining any release error with
the composition error, or transfers them to the App for `Close`.

The dashboard is optional: leave `SetSPAFactory` unset, or pass nil to remove
the one `Register` installed. Unmatched paths then answer the typed JSON 404.
A configured factory must return a non-nil filesystem or an error.
`app.RouteConfig.NotFound` and `MethodNotAllowed` pass through to the router;
their contract is in `framework/web/httpapi.RouterConfig`. The router refuses
a `NotFound` hook together with a SPA.

A host that composes its own `app.RouteConfig` sets its required `Routes`
registry and `AccessMode` callback, called on every route manifest request,
and the `Access` of each extra mount, `public` or `agent`.
`BuildServeHandler` fails without them, and on
an extra mount or an MCP path at or under any surface prefix, including
Officer's `/api/v1` and `/app/api/v1`. A route marked `Public` is valid only on
a `SourcePanel` surface; `NewRouter` panics on it elsewhere. `AccessMode` and
`Access` are reported by the route manifest.
`DuringSetup` is also reported, and Officer's resolver and authorizer enforce
it in `setup`: only a panel route marked with it is admitted. The flag does not
open the corresponding `/api/v1` route. Public panel routes bypass caller
resolution and the authorizer, so their handlers must enforce any setup
restriction themselves.

`BuildServeHandler` also refuses an operation mounted twice: each method and
path, parameter names ignored, may be mounted only once across the surfaces,
the route manifest, the MCP methods, the extra mounts, and the dashboard's
`GET /*` when enabled.

A host extends the route surface `Register` installs by wrapping
`builder.RouteConfigBuilder()` and setting the wrapper with
`SetRouteConfigBuilder`, as `examples/customhost/main.go` does, so the account
mode and the access mode stay served. Append prefixes to `RouteConfig.Surfaces`;
`BuildServeHandler` mounts them after Officer's V1 pair, through the same caller
resolver and authorizer. The example serves `/example/api/v1/host` this way,
with its own route ID and the existing host handler, without a separate router.

`Register` adds a node hook that builds the access service whatever factories
a host replaces. A store without Officer's access state or seal-report reader
fails `Build`; nothing serves without the access service. A host replacing the
service factory gets a backend without Officer's account scope and should not
serve the account-mode routes Register adds: a stored mode would never take effect.

A host that calls the control plane itself, outside a request, puts a caller
in the context first (`auth.ContextWithCaller`): every call that reads or
changes account-owned data asks the account scope about that caller and is
refused without one, in every account mode.

`examples/customhost` is the worked example: it composes the installed
authorizer, adds a market-data provider, and adds, replaces, and removes MCP
tools and REST routes on top of the default composition.

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
the runtime: `tidy` and `fmt-go` on the Go side; `ensure-node`, `build-js`,
`build-js-lib`, `fmt-js`, `lint-js`, `test-js`, `check-js`, and `check-js-dry` on
the frontend side; and `fmt-python`, `lint-python`, and `check-semgrep` on the
Python and Semgrep side.

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

Build identity comes from two link-time variables in
`go.openpit.dev/officer/framework/backend`: `buildCommit` holds the full
commit ID, and a non-empty `releaseTag` marks an official release build.
Only the official image workflow for a version tag sets `releaseTag`; local
images, `edge` and release dry runs are non-release builds. Building a tagged
checkout locally with `just build` still produces a non-release binary.
The binary build recipes, including the debug and `-dev` variants, set
`buildCommit` from `git rev-parse HEAD` and leave `releaseTag` empty. Their
`-release` suffix selects an optimization profile, not a release identity.

The release tag also gives the version MCP clients see as
`serverInfo.version`: the tag without its leading `v`, so `v0.2.1` reports
`0.2.1`. A non-release build reports `dev`. A non-empty `releaseTag` that is
not `v<major>.<minor>.<patch>`, optionally with a pre-release or build suffix,
stops `serve` and `mcp` at startup. The start log names the version in its
`mcp server version` record, with `release_build` saying whether the build is a
release.

```bash
cd web && npm ci && npm run build && cd ..
CGO_ENABLED=1 go build -ldflags "-X go.openpit.dev/officer/framework/backend.buildCommit=$(git rev-parse HEAD)" -o pit-officer ./cmd/pit-officer
```

This is a non-release build. Its `serve` and `mcp` subcommands refuse to start
without a valid `buildCommit`: 40 or 64 lowercase hexadecimal characters.
Plain `go build` without the link-time assignment produces a non-release
binary that reports `missing or invalid build commit`, with instructions to
use `just build` or the `-ldflags` assignment above. `dashboard` and
`healthcheck` do not validate the commit. For the database choice and file
lifetime, see [Non-release builds](configuration.md#non-release-builds).

Build the SPA first. `//go:embed dist` takes whatever is in that directory
at build time, and a fresh checkout carries only the placeholder file there; a
binary built over the placeholder has no `index.html` to serve, and `serve`
fails when it builds the SPA handler at startup.

## Frontend

`just ensure-node` installs the Node.js version `CI_NODE` pins in
`.github/ci-versions.env` into `.tmp/node`. Every recipe that runs node, npm or
npx depends on it and runs that runtime, whatever node is on the caller's PATH.

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
`just lint-python`, `just check-semgrep`, `just test-go`, `just test-js`. The `-dry` variants lint
and test without rebuilding artifacts.

`just build-go` only compiles the Go packages and produces no binary; use it as
a compile check.

Semgrep and ruff run from a virtualenv in `.venv`, created and populated by
`just install-python-tools` from `requirements.txt` on the first run and skipped
afterwards while the installed versions match the pins.

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

`Dockerfile` requires `OFFICER_COMMIT` and passes it to `buildCommit`;
`just docker-build` supplies the checkout's full commit ID. The optional
`OFFICER_RELEASE_TAG` is passed to `releaseTag` and defaults to empty.

The build needs BuildKit, the default builder in current Docker, because the SPA
stage runs on the build platform via `$BUILDPLATFORM`.

Every push to `main` that passes CI publishes the image for `linux/amd64` and
`linux/arm64` to `ghcr.io/openpitkit/officer` as `edge` and
`sha-<short sha>-<run>.<attempt>`, then deletes the main images it supersedes:
the package keeps one main image, the current `edge`.

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
container to become healthy and `/healthz` to answer 200. Then it deletes every
dry-run image, its own included, so no dry-run image outlives its run. Last, it
creates a draft GitHub release under the dry-run tag name and deletes it at
once; a draft creates no git tag. The `main` check, the release notes check and
the ranking of `X.Y` and `latest` run only on a real version tag.

`just docker-start-check <image> <platform>` runs the same start check on a
local image, for example `just docker-start-check pit-officer:local
linux/arm64`.

## Releases

For build identity, see [Building without Just](#building-without-just).

A release is cut on `main` as a `major`, `minor` or `patch` bump of the highest
stable `vX.Y.Z` tag merged into `HEAD`; before the first release that version is
`0.0.0`, so the first `minor` release is `0.1.0`. `just release-version <kind>`
prints the version a bump cuts. The notes are kept outside the checkout; the
file starts with `<!-- release-version: X.Y.Z -->`, that exact version without
the `v`, and the notes follow that line.

```bash
just release-version minor        # 0.1.0
just release minor /absolute/path/notes.md
git push --atomic origin main v0.1.0
```

`just release` runs `just check`, copies the notes to `RELEASE_NOTES.md`,
commits them as `Release X.Y.Z`, tags that commit `vX.Y.Z` and pushes nothing.
The tag starts the release workflow. Before it publishes anything, it requires
`RELEASE_NOTES.md` for the tag's version in the tagged commit, the same check
`just check-release-notes X.Y.Z` runs locally. It then publishes the image as
described above and creates the GitHub release, with the notes after the marker
line as its description. A stable version is marked latest only when the
image's `latest` moves to it. A pre-release tag `vX.Y.Z-rc.N`, which
`just release` does not cut, still needs its notes committed and becomes a
GitHub pre-release.

## Seeding

`just seed` passes no options; it fills a running instance with demo accounts,
balances, orders, and trades through its API, finding the instance from
`officer-runtime.json` or `officer-runtime.non-release.json`.
When both files are present, run `python3 tools/seed/seed.py --runtime-file PATH`
directly, or replace `--runtime-file PATH` with `--base URL`.
