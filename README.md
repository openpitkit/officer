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
in the `pit-officer-data` volume.

`edge` is the current development build. It may be unstable, so run it apart
from the release, with its own name, port and data:

```sh
docker run -d --name pit-officer-edge --restart unless-stopped -p 127.0.0.1:8788:8787 -v pit-officer-edge-data:/data ghcr.io/openpitkit/officer:edge
```

Then open <http://127.0.0.1:8788>.

## Build

Prerequisites: a C toolchain, which the OpenPit Go binding requires through
cgo; Go; and Node and npm for the dashboard bundle. Tool versions are pinned in
`.github/ci-versions.env`.

From the repository root, build the dashboard before the binary so Go embeds
the compiled assets:

```bash
npm --prefix web ci
npm --prefix web run build
CGO_ENABLED=1 go build -o pit-officer ./cmd/pit-officer
```

On Windows, set `CGO_ENABLED=1` in your shell, build with
`go build -o pit-officer.exe ./cmd/pit-officer`, and run `./pit-officer.exe`.

### Run from source

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
docker build --build-arg GO_VERSION="$CI_GO" -t pit-officer:local .
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
