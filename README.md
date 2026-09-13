# Pit Officer

[![CI](https://github.com/openpitkit/officer/actions/workflows/ci.yml/badge.svg)](https://github.com/openpitkit/officer/actions/workflows/ci.yml) [![Go version](https://img.shields.io/badge/go-1.25.11%2B-00ADD8)](https://pkg.go.dev/go.openpit.dev/officer) [![Module](https://img.shields.io/badge/module-go.openpit.dev%2Fofficer-00ADD8)](https://pkg.go.dev/go.openpit.dev/officer) [![License](https://img.shields.io/badge/license-Apache%202.0-blue)](https://github.com/openpitkit/officer/blob/main/LICENSE)

Pit Officer is a control plane for the embeddable [OpenPit](https://openpit.dev)
pre-trade risk engine. It runs one engine in a local process over a local SQLite
database and exposes it to operators through a web dashboard, to programs
through a REST API, and to AI agents through an MCP server. The engine alone
evaluates orders.

## Capabilities

- **One local engine over one local store** - a single node built from SQLite at startup.
- **A reusable framework**: the importable Go packages - [`go.openpit.dev/officer`](https://pkg.go.dev/go.openpit.dev/officer) with its `web`, `httpapi`, `mcptools`, `engine`, `signing`, and `framework/...` packages - and `web/src/framework/` in React. This application is one composition of them; a program can build its own.
- **A REST API over the control plane** - see the [API notes](docs/api.md).
- **An MCP server** over stdio or streamable HTTP, with mutating tools off until an operator enables them by name.
- **An operator dashboard**, an embedded single-page app driven by the same API.
- **Signed order approvals** - an Ed25519 approval envelope for every order that passes pre-trade.
- **An append-only audit trail** of every control-plane action.
- **Market data through pluggable connectors**, configured and toggled at runtime.
- **Optional at-rest encryption** of stored secrets under an operator-supplied master key.

## Run

Pit Officer is a single binary with four subcommands: `serve`, `mcp`,
`dashboard`, `healthcheck`. With [Just](https://just.systems/):

```bash
just run-serve   # build the SPA and the binary, then serve
just dashboard   # print the running instance's URLs and open the browser
just run-mcp     # local stdio MCP server
```

In a container, build the image with `just docker-build` and start it with
`docker compose up`; `docker-compose.yml` pins the port, the database volume,
and a read-only root filesystem.

The subcommands, the environment variables and flags, and the authorization
model are in the [configuration reference](docs/configuration.md).

## Build

Prerequisites: a C toolchain with cgo enabled, which the OpenPit Go binding
requires; Go; Node and npm, for the dashboard bundle; Python 3, which the
recipes use to drive Go, the linters, and Semgrep; golangci-lint; and
[Just](https://just.systems/). Tool versions are pinned in
`.github/ci-versions.env`.

```bash
just frontend-ci-install  # npm ci in web/ from the committed lockfile
just build                # build the SPA, then the pit-officer binary
just check                # format checks, linters, Semgrep, tests, frontend bundles
```

Composing your own Officer from the importable packages, building against a
local Pit checkout, building without Just, and the debug variants are covered in
the [development notes](docs/development.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
