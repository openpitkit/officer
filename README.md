# Pit Officer

[![CI](https://github.com/openpitkit/officer/actions/workflows/ci.yml/badge.svg)](https://github.com/openpitkit/officer/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/openpitkit/officer)](https://github.com/openpitkit/officer/releases) [![License](https://img.shields.io/badge/license-Apache%202.0-blue)](https://github.com/openpitkit/officer/blob/main/LICENSE)

Website: [officer.openpit.dev](https://officer.openpit.dev)

Pit Officer lets you manage trading accounts, set risk limits, connect to
brokers, and send orders from a web panel. The [OpenPit](https://openpit.dev)
risk engine checks each order before Officer sends it through a trading
connection. You can also use Officer from trading software or AI agents.

## Run

For a visual walkthrough, see the [video guides](https://officer.openpit.dev/video-guides).

Officer runs on your own computer, and you use it through a web browser.
Docker lets you run the same ready-made application on Windows, macOS, and
Linux, without building it or installing programming tools.

If you do not have Docker yet, follow the
[Docker installation guide](https://docs.docker.com/get-started/get-docker/).
Start Docker Desktop and wait until it says it is running. On Linux, you can
also use Docker Engine; see the [quickstart](docs/quickstart.md#install-and-start-docker).

Open a terminal, the window where you type commands: **PowerShell** from the
Start menu on Windows, **Terminal** on macOS, or your terminal application on
Linux. Copy the command below, paste it there, and press Enter. The command
is the same on all three systems:

```sh
docker run -d --name pit-officer --restart unless-stopped -p 127.0.0.1:8787:8787 -v pit-officer-data:/data ghcr.io/openpitkit/officer:latest
```

`latest` is the newest stable release of Officer. The first run downloads
Officer, which may take a few minutes. It then runs in the background, so you
can close the terminal. Your settings and records are saved on your computer
in storage named `pit-officer-data` and kept when you stop or restart Officer.

To check whether it is ready, run:

```sh
docker ps --filter name=pit-officer
```

When the STATUS column shows `(healthy)`, open <http://127.0.0.1:8787> in your
browser and bookmark it. If it shows `(health: starting)`, wait a few seconds
and check again. The panel is ready for you to configure your accounts, risk
limits, and connections.

In that address, `127.0.0.1` means your own computer, and `8787` is the port,
the number used to reach the panel. To use a different port, choose it before
running the long command: for example, replace `-p 127.0.0.1:8787:8787` with
`-p 127.0.0.1:8788:8787`, then open <http://127.0.0.1:8788>. Keep the final
`8787` unchanged; it is the number Officer uses inside Docker.

To stop Officer, run `docker stop pit-officer`. To start it again, run
`docker start pit-officer` and open your bookmark. Use these commands after
the first installation; you do not need to repeat the long command.

To try changes before they are released, run the `edge` build instead: the
newest version under development that passed its checks. It changes often and
may not work, so give it its own name, port, and storage, apart from your
stable installation:

```sh
docker run -d --name pit-officer-edge --restart unless-stopped -p 127.0.0.1:8788:8787 -v pit-officer-edge-data:/data ghcr.io/openpitkit/officer:edge
```

Then open <http://127.0.0.1:8788>, and use `pit-officer-edge` instead of
`pit-officer` in the commands above.

For help with installation, changing the port of an existing installation,
or a panel that will not open, see the [quickstart](docs/quickstart.md).

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
