# i18n

Internationalization for the Pit Officer SPA, built on
[react-i18next](https://react.i18next.com/). Phase 1 stands up the framework,
the language switcher, the lint guardrail, and one migrated pilot page
(`pages/Service.tsx`). Only the `en` catalog exists so far.

## How it is wired

- `index.ts` initializes i18next with the React binding and the browser
  language detector. **Resources are discovered by glob** —
  `import.meta.glob('./locales/**/*.json', { eager: true })` — and folded into
  `{ [locale]: { [namespace]: json } }` from each path
  `./locales/<locale>/<namespace>.json`. Adding a namespace or language file
  needs **no edit** to `index.ts`.
- `locales.ts` is the supported-locale list (`{ code, endonym }`). Adding a
  language is a one-line entry plus a `locales/<code>/` catalog folder.
- `LocaleProvider.tsx` mirrors the active language onto `<html lang>`, the same
  way `theme/ThemeProvider` mirrors the palette. Detection and persistence are
  owned by the detector (localStorage key `pit-officer-lang`); a manual choice
  in the switcher overrides auto-detection on the next load.
- `format.ts` holds locale-aware formatting helpers.

## Conventions for the extraction phases

### Namespaces

One namespace per area, named after the page/feature (`service`, `accounts`,
`policies`, …). Shared chrome — generic verbs, placeholders, the language
switcher — lives in `common` (the default namespace). Components opt into an
area namespace with `useTranslation("<area>")` and reach `common` with a second
`useTranslation()`.

### Key naming

Hierarchical and human-meaningful, grouped by UI region, e.g.
`api.openApiDocs`, `database.unreachable`. Keys describe the slot, not the
English wording, so a reworded string keeps its key.

### Formatting — use `format.ts`, not raw `toLocale*` / `new Date()`

Render dates and counts through `formatDate` / `formatTime` / `formatDateTime`
/ `formatNumber`. They bind `Intl` formatters to the active UI language and
update live with the switcher.

**Do not** route API decimals (prices, quantities, P&L) through
`formatNumber`: they arrive as strings to preserve precision, and
`Number(...)`-parsing them would lose it. Render those strings as-is.

## The cross-layer vocabulary rule (must follow)

`api/vocabulary.ts` defines **domain identifiers** — `POLICIES`, `SCOPES`, and
kinds like `rate_limit`, `broker`, `max_orders`. These are byte-identical
cross-layer constants shared with the Go domain, the SQL data, and the
REST/MCP JSON. **They MUST NEVER be translated, re-cased, or renamed.**

Only their *display labels* become translation keys: `POLICY_LABELS`,
`SCOPE_LABELS`, and the `label` / `description` / `hint` fields of
`POLICY_CATALOG`. The suggested approach for a later phase is a `domain`
namespace keyed **by identifier**, e.g.

```jsonc
// locales/en/domain.json
{
  "policy": { "rate_limit": { "label": "Rate limit", "description": "…" } },
  "scope": { "broker": "Broker" }
}
```

looked up as `t("domain:policy.rate_limit.label")` with the raw `rate_limit`
identifier still flowing unchanged through the wire and the catalog maps.

Do **not** migrate `vocabulary.ts` or its consumers in phase 1 — this section
only records the intended approach.

## Lint guardrail

`web/eslint.config.js` enables `i18next/no-literal-string` at **"warn"** for
this phase. Un-migrated files are expected to warn; a later phase drains the
warnings to zero and flips the rule to **"error"**. The migrated pilot
(`Service.tsx`) produces no `no-literal-string` warnings and is the template to
copy.
