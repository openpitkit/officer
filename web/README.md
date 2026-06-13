# Pit Officer dashboard (web)

The operator single-page app for Pit Officer. It is built with Vite and embedded
into the Go binary via `go:embed web/dist`, then served from `/` by the Pit Officer
HTTP layer in `serve` mode. The SPA consumes the `/api/v1` control-plane surface:
read-only status (`GET /api/v1/status`, `GET /api/v1/health`) plus the accounts,
limits, and audit endpoints. It carries no risk logic; all validation is mirrored
from the backend contract only to give fast form feedback.

## Pages

- **Dashboard** (`/`) - live engine/store health.
- **Accounts** (`/accounts`) - create accounts, block/unblock with a reason.
- **Limits** (`/limits`) - relational risk barriers per policy/scope, filtered
  by account and policy, with an add/edit dialog and delete confirmation.
- **Audit** (`/audit`) - the change log, newest first, with a page-size select.

Client-side routing is provided by `react-router-dom`; the sidebar reflects the
active route.

## Stack

- React 19 + TypeScript
- Vite 6 (build to `dist/`, base `/`)
- `react-router-dom` 7 for routing
- Tailwind CSS 3 with design tokens ported from the OpenPit website
- Radix primitives + hand-written shadcn-style components (`src/components/ui`)
- JetBrains Mono bundled locally via `@fontsource` (no external font CDN)

## Develop

```bash
npm install      # package.json gained react-router-dom + Radix dialog/select
npm run dev      # Vite dev server on :5173, proxies /api -> 127.0.0.1:8787
```

Run the Go backend pinned to the proxied port alongside, so the dashboard has a
live `/api/v1/status` to read:

```bash
pit-officer serve -http-addr 127.0.0.1:8787
```

By default `serve` binds a free OS-assigned port; the dev proxy expects the
fixed `127.0.0.1:8787`, so pin it explicitly while developing the SPA.

## Build

```bash
npm run build    # type-checks then emits the embeddable bundle into dist/
```

The build overwrites the committed `dist/index.html` placeholder. That
placeholder (and `dist/.gitkeep`) are kept in version control so the Go
`go:embed web/dist` compiles on a fresh clone before the SPA is built.

## Theme

Three modes - dark (default palette), light, and system - selected from the top
bar and persisted to `localStorage` under `pit-officer-theme`. `ThemeProvider`
toggles a `.dark` / `.light` class on `<html>`; in system mode it follows
`prefers-color-scheme`. `index.html` applies the stored theme before first paint
to avoid a flash of the wrong palette.
