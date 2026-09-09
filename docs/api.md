# API notes

What a client needs beyond the endpoint reference: where the surface is mounted,
what it answers with, and the two error shapes.

## Mount points

REST is mounted at `/api/v1`. A mirror at `/app/api/v1` is reserved for the
operator panel. The two carry the same routes except the route manifest, which
is published only on `/api/v1`; otherwise they differ only in the source stamped
on the audit record.

## Discovering the surface

The OpenAPI 3.1 document describes the `/api/v1` surface. It is served at
`/api/openapi.yaml` and browsable at `/docs`. It is hand-written and embedded in
the binary, and what lives outside `/api/v1` - liveness, the document itself,
the docs page, MCP over HTTP - is not in it.

`/docs` loads Swagger UI from a public CDN, so it needs outbound internet and is
blank on an isolated host. The document at `/api/openapi.yaml` is served from
the binary and needs nothing.

## Media types

Control endpoints answer `application/json`. Endpoints that hand back a file -
the backup and business-CSV exports, the service-log download - answer with that
file's own media type as an attachment.

## Errors

Two shapes, decided by what failed:

- A malformed request body or a request that violates the schema or a parameter
  constraint answers RFC 9457 `application/problem+json`:
  `{type, title, status, detail, errors: [{code, constraint, pointer}]}`, with
  `400` for broken syntax and `422` for a constraint violation.
- Every other error answers `application/json` with
  `{"error": {"code": "...", "message": "..."}}`.

## Values

Money and size values cross the wire as exact decimal strings, not as IEEE 754
floats.

## MCP tools

The tool catalog is served by the API and shown in the operator panel. It
records, per tool, what it does, whether it mutates state, and whether it is
implemented - a catalogued name that is not implemented is listed but not
callable.

Non-mutating tools are enabled by default. Every tool that changes state ships
disabled, stays disabled until an operator enables it by name, and refuses the
call while it is off.

## Audit source

Every control-plane action is recorded with the surface it arrived on. The
source is derived from the mount point that served the request, never from a
client-supplied header or body: `/api/v1` stamps the API source and
`/app/api/v1` the panel source, so a client selects the recorded source by
selecting the path.
