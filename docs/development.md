# Development notes

Everything a contributor needs beyond the build in the README: the developer-only
recipes, and how they differ from the default ones.

## The framework module

The root module is the application. `framework/` is the module a program imports
to build a composition of its own:

```bash
go get go.openpit.dev/officer/framework
```

Before `1.0` the public interface is not stable: a minor version may change it,
a patch version carries fixes only. Pick a version constraint that tolerates
that.

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

Build the SPA first. `//go:embed web/dist` takes whatever is in that directory
at build time, and a fresh checkout carries only the placeholder file there; a
binary built over the placeholder has no `index.html` to serve, and `serve`
fails when it builds the SPA handler at startup.

## Modules and tidying

The tree holds two Go modules: the root, and `framework/`, which the root
requires through a `replace` directive. `just tidy` tidies `framework` first and
the root second. The other order leaves the root `go.sum` computed against the
previous framework requirements whenever a dependency moves between them.

## Frontend

`just frontend-ci-install` installs from the committed lockfile (`npm ci`);
`just frontend-install` runs `npm install` and is what the binary build recipes
use. `just build-js` builds the SPA into `web/dist`, and `just build-js-lib`
builds the publishable framework library into `web/lib`. Only
`web/dist/embed-placeholder.txt` is committed; the rest of `web/dist` is build
output, and `//go:embed web/dist` takes whatever is in that directory when the
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

## Seeding

`just seed` fills a running instance with demo accounts, balances, orders, and
trades through its API.
