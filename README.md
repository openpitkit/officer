# Pit Officer

[![CI](https://github.com/openpitkit/officer/actions/workflows/ci.yml/badge.svg)](https://github.com/openpitkit/officer/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/openpitkit/officer)](https://github.com/openpitkit/officer/releases) [![License](https://img.shields.io/badge/license-Apache%202.0-blue)](https://github.com/openpitkit/officer/blob/main/LICENSE)

Website: [officer.openpit.dev](https://officer.openpit.dev)

Pit Officer lets you manage trading accounts, set risk limits, connect to
brokers, and send orders from a web panel. The [OpenPit](https://openpit.dev)
risk engine checks each order before Officer sends it through a trading
connection. You can also use Officer from trading software or AI agents.

## Run

Watch the [video guide](https://officer.openpit.dev/video-guides#guide-quickstart)
or follow the steps below. The [quickstart](docs/quickstart.md) covers
stopping, ports and troubleshooting.

Install [Docker](https://docs.docker.com/get-started/get-docker/), then run:

```sh
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer:latest
```

Open <http://127.0.0.1:8787>. `latest` is the newest release; your data stays
in the `pit-officer-data` volume. Back up before every upgrade, and regularly,
from Service > Database > Database backup > Export in the panel.

To upgrade an installed Officer, see [Upgrade Officer](docs/quickstart.md#upgrade-officer).

`edge` is the current development build, for quick tests only. Its data is
disposable. Stop the release container first, then use the same volume:

```sh
docker stop pit-officer
docker run -d --name pit-officer-edge --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer:edge
```

On its first start, the panel asks whether to copy the release data or start
empty. It never changes the release database. See
[Trying the development build](docs/quickstart.md#trying-the-development-build).

## Build

Prerequisites: a C toolchain, which the OpenPit Go binding requires through
cgo; Go; and Node and npm for the dashboard bundle. Tool versions are pinned in
`.github/ci-versions.env`; `just ensure-node` installs the pinned Node into
`.tmp/node`.

From the repository root, build the dashboard before the binary so Go embeds
the compiled assets:

```bash
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=1 go build -ldflags "-X go.openpit.dev/officer/framework/backend.buildCommit=$(git rev-parse HEAD)" -o pit-officer ./cmd/pit-officer
```

On Windows with PowerShell, set `CGO_ENABLED=1` in your shell, build with
`go build -ldflags "-X go.openpit.dev/officer/framework/backend.buildCommit=$(git rev-parse HEAD)" -o pit-officer.exe ./cmd/pit-officer`, and run `./pit-officer.exe`.

### Run from source

A locally built binary behaves like `edge`: its first `serve` asks which
database to use, and it never changes the release database. Make that
[choice](docs/configuration.md#non-release-builds) before running `mcp`.

Pit Officer is a single binary with four subcommands: `serve`, `mcp`,
`dashboard`, `healthcheck`. After building it, run from the repository root:

```bash
./pit-officer serve       # serve the dashboard, REST API, and HTTP MCP
./pit-officer dashboard   # in another terminal, open the running instance
./pit-officer mcp         # alternatively, run a local stdio MCP server
```

The subcommands, environment variables, flags, and authorization model are
in the [configuration reference](docs/configuration.md). See the
[API notes](docs/api.md) for REST and MCP integration.

### Local container build

To build and run a local container image with the settings in
`docker-compose.yml`:

```bash
. ./.github/ci-versions.env
docker build --build-arg GO_VERSION="$CI_GO" --build-arg OFFICER_COMMIT="$(git rev-parse HEAD)" -t pit-officer:local .
docker compose up
```

### Optional Just shortcuts

[Just](https://just.systems/) provides a quicker alternative to the manual
steps above. The recipes also require Python 3; the full check additionally
uses golangci-lint and Semgrep, as described in the
[development notes](docs/development.md#gates).

```bash
just frontend-ci-install  # npm ci in web/ from the committed lockfile
just build                # build the SPA, then the pit-officer binary
just run-serve            # build and serve
just dashboard            # open the running instance in the browser
just run-mcp              # build and run a local stdio MCP server
just docker-build         # build the pit-officer:local container image
just check                # format checks, linters, Semgrep, tests, frontend bundles
```

Composing your own Officer from the importable packages, building against a
local Pit checkout, and the debug variants are covered in
the [development notes](docs/development.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
