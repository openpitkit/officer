# Pit Officer dashboard and web framework

This package has two surfaces:

- `@openpit/officer-web` is the reusable React framework library exported from
  `src/index.ts`.
- The Pit Officer single-page app is the reference consumer under `src/`.

The app is built with Vite and embedded into the Go binary via
`//go:embed dist` in `web/dist.go`, then served from `/` by the Pit Officer HTTP layer in
`serve` mode. The SPA consumes the `/app/api/v1` control-plane surface in the
embedded build. It carries no risk logic; all validation is mirrored from the
backend contract only to give fast form feedback.

Client-side routing is provided by `react-router-dom`; routes are registered in
`src/register.ts`, and the sidebar reflects the active route.

## Stack

- React 19 + TypeScript
- Vite 6 (build to `dist/`, base `/`)
- `react-router-dom` 7 for routing
- Tailwind CSS 3 with design tokens ported from the OpenPit website
- Radix primitives + hand-written shadcn-style components (`src/components/ui`)
- JetBrains Mono bundled locally via `@fontsource` (no external font CDN)

## Framework library

The framework barrel is `@openpit/officer-web`:

```ts
import {
  ApiClientProvider,
  createApiClient,
  createOfficerApi,
  useOfficerApi,
} from "@openpit/officer-web";
```

The package is publishable and can also be consumed from a local workspace or
`file:` dependency before registry publication. The library build emits ESM and
TypeScript declarations into `web/lib`:

```bash
npm run build:lib
```

Hosts inject API transport through `ApiClientProvider`:

```tsx
<ApiClientProvider
  config={{
    baseUrl: "https://host.example/app/api/v1",
    fetch: customFetch,
    headers: { "X-Client": "officer" },
    getHeaders: async () => ({ Authorization: await bearerToken() }),
    translate: (key, params) => i18n.t(key, params),
  }}
>
  <App />
</ApiClientProvider>
```

`baseUrl`, `fetch`, static `headers`, async per-request `getHeaders`, and
`translate` are supplied by the host. The app passes
`baseUrl: "/app/api/v1"` and `translate: i18n.t`, and injects no additional
headers.

The app still carries a temporary compatibility shim at `src/api/client.ts`
for existing page and hook imports. The shim is app-bound and delegates to the
same `createApiClient` and `createOfficerApi` factories; no request transport or
base URL is implemented there. New framework consumers should import from
`@openpit/officer-web`.

## App composition

The SPA is a thin framework consumer. `src/main.tsx` initializes i18n,
calls `registerOfficerDefaults`, injects the same-origin API client with
`baseUrl: "/app/api/v1"`, mounts the allow-all `AuthProvider`, and renders the
framework shell.

`src/register.ts` defines `registerOfficerDefaults`, the single composition
function, whose contract is described in [Consuming the web library](#consuming-the-web-library).

`src/App.tsx` wraps the framework `Sidebar` and `AppRoutes` in the app's
startup flow. `StartupGate` asks `GET /session` first. In the `setup` state,
while the first-run setup is not finished, it admits the browser without a
login, and every path goes to the full-screen wizard at `/setup`
(`src/pages/Setup.tsx`), outside the shell. In `token` access mode it shows the
login or password recovery screen until the browser has a session, and a
`401 unauthorized` from any request returns it there. It then reads
`GET /account-mode` before rendering and keeps polling it; a restart into
another account mode, or a mode the first-run setup took at once, reloads the
page. A reset of the database returns the
panel to the wizard. After setup, `/setup` opens the same wizard in review
mode, from About > First-run guide and Service > Security. App code asks
`accountScope()` (`src/lib/accountScope.ts`) whether accounts are shown and
which account a request carries, never whether the mode is single;
`src/register.ts` switches the Accounts navigation entry and page through
`when` predicates.

Service > Security shows and changes the access mode, the panel password,
browser sessions and both tokens; its storage section shows encryption with
the master key guide, decision signing and the account mode; its guide section
reopens the first-run setup. Connect your agent carries the agent token in every
connect command and configuration in `token` access mode.

## Consuming the web library

The reusable package is `@openpit/officer-web`, exported from
`src/index.ts` and built with `npm run build:lib` into `web/lib`.
The SPA is a thin consumer of the same library: `src/main.tsx` calls its
`registerOfficerDefaults` export, injects `baseUrl: "/app/api/v1"`, mounts the
allow-all `AuthProvider`, and renders the framework shell.

Importing the package initializes the host-shared i18next instance with the
panel's locale catalogs and language detector. It registers no routes, page
overrides, navigation, widgets, row actions, or vocabulary.

A host calls `registerOfficerDefaults` once before rendering to register the
open panel's routes, page overrides, navigation, widgets, row actions,
vocabulary, and locale resource map. After the call, it reads stable ids through
`getRoutes()`, `getNav(section)`, `getPage(id)`, `getWidgets()`,
`getRowActions(scope)`, `getScopes()`, and `getPolicies()`, then replaces an entry
by re-registering its id or removes it with the matching unregister API.

The caller must load the panel's account scope and session before rendering
these pages or evaluating navigation predicates. They read
`src/lib/accountScope.ts` and `src/lib/panelSession.ts`, which throw while not
loaded; the panel's `StartupGate` loads both before the shell renders.

`react`, `react-dom`, `react-router-dom`, `i18next`,
`i18next-browser-languagedetector` and `react-i18next` are peer dependencies:
the host installs them and the framework shares the host's instance, so
`AppRoutes` and `Sidebar` render inside the host's router. The panel
stylesheet is published separately and imported once by the host:

```ts
import "@openpit/officer-web/styles.css";
```

The stylesheet names JetBrains Mono but does not carry its font files: the host
loads the 400, 500 and 700 weights itself, as `src/main.tsx` does with
`@fontsource/jetbrains-mono`.

The package also publishes the Pit Officer favicon as
`@openpit/officer-web/favicon.svg`, the same file the panel serves at
`/favicon.svg`. A host links it instead of copying it; for example, a Vite host
imports it as an asset URL.

Host applications provide transport through `createApiClient`,
`createOfficerApi`, and `ApiClientProvider`. `ApiClientProvider` accepts the
base URL plus optional `fetch`, static `headers`, async `getHeaders`,
`translate`, and `onUnauthorized` hooks, so a consuming app can route requests
through its own origin, proxy, or authenticated fetch implementation;
`onUnauthorized` runs once for every failed response whose error code is
`unauthorized`, before the `ApiError` is thrown. A failed call's
`ApiError.retryAfterSeconds` carries the response's `Retry-After` in seconds.

The UI extension points are stable-id registries exported from the package
root. Routes use `registerRoute`, `getRoutes`, and `unregisterRoute`; page
overrides use `registerPage`, `getPage`, and `unregisterPage`; navigation uses
`registerNav`, `getNav`, and `unregisterNav`; dashboard widgets use
`registerWidget`, `getWidgets`, and `unregisterWidget`; row actions use
`registerRowAction`, `getRowActions`, and `unregisterRowAction`; policy and
scope vocabulary uses `registerPolicy`, `registerScope`, `unregisterPolicy`,
and `unregisterScope`; locale namespaces use `registerLocaleResources` or
`registerLocaleResourceMap`. Re-registering the same id replaces an entry,
unregistering removes it, and permission fields hide rendered routes, nav
entries, pages, and row actions without removing them.

Authorization is supplied by `AuthProvider`. It is required above every
component or hook that reads authorization (`Sidebar`, `AppRoutes`,
`RegistryRowActions`, `useAuth`, `useUser`, `useHasPermission`), which throw
without it. Mounted without props it grants every permission; the app mounts it
that way, while a host application can pass a custom `hasPermission` predicate
or a complete `AuthContextValue`; renderers consume it through
`useHasPermission`.

The reference composition under `src/examples/customhost` imports only
`@openpit/officer-web` for registry and auth surfaces. It adds a private page,
nav entry, widget, row action, vocabulary, and locale namespace; replaces a page
and widget by re-registering ids; hides an action through a custom
`AuthProvider`; and removes a route through the unregister API. It is compiled
by `npm run typecheck`, covered by Vitest, and is not imported by `src/main.tsx`,
so it never enters the embedded `web/dist` SPA bundle.

## Develop

```bash
npm install      # install dependencies from package.json
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
npm run build:lib # emits the framework package into lib/
npm run test:lib  # checks the built package; run build:lib first
```

The build writes generated assets into `dist/`. The generated bundle is ignored,
but a stable `dist/embed-placeholder.txt` file is kept in version control so
the Go `//go:embed dist` in `web/dist.go` compiles on a fresh clone before the SPA is built.
The library build writes only to `lib/` and never clobbers the embedded app
bundle in `dist/`.

## Theme

Three modes - dark (default palette), light, and system - selected from the top
bar and persisted to `localStorage` under `pit-officer-theme`. `ThemeProvider`
toggles a `.dark` / `.light` class on `<html>`; in system mode it follows
`prefers-color-scheme`. `index.html` applies the stored theme before first paint
to avoid a flash of the wrong palette.
