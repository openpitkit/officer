import js from "@eslint/js";
import i18next from "eslint-plugin-i18next";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";
import globals from "globals";
import tseslint from "typescript-eslint";

// Flat config for the Pit Officer SPA. Standard Vite + React + TypeScript
// linting plus the i18n hardcoded-string guardrail.
export default tseslint.config(
  { ignores: ["dist", "node_modules"] },
  {
    files: ["src/**/*.{ts,tsx}"],
    extends: [
      js.configs.recommended,
      ...tseslint.configs.recommended,
      i18next.configs["flat/recommended"],
    ],
    languageOptions: {
      ecmaVersion: 2022,
      globals: globals.browser,
    },
    plugins: {
      "react-hooks": reactHooks,
      "react-refresh": reactRefresh,
    },
    rules: {
      // react-hooks recommended set keeps `set-state-in-effect` and `refs` at
      // their `error` severity; intentional, behavior-preserving uses carry a
      // narrow per-line disable at the call site rather than a global downgrade.
      ...reactHooks.configs.recommended.rules,
      // `allowConstantExport` exempts constant value exports (the legitimate
      // shadcn/ui `*Variants` co-location) so the rule need not be downgraded;
      // genuine component+hook splits carry a narrow per-line disable.
      "react-refresh/only-export-components": [
        "error",
        { allowConstantExport: true },
      ],
      // The i18n rollout is complete: every user-facing string goes through
      // t(), so this guardrail is now an ERROR. The allowlist below keeps it
      // from firing on legitimately non-UI literals rather than relying on
      // scattered inline disables.
      "i18next/no-literal-string": [
        "error",
        {
          // Flag user-facing JSX text only, not structural strings or string
          // literals in code (variant names, enum values, JSON field keys).
          mode: "jsx-text-only",
          // Calls whose string arguments are data/identifiers, never UI copy:
          // the t()/i18n helpers themselves, the api/ JSON-field & coercion
          // accessors (pick/asString/…), and the className composers. In
          // jsx-text-only these are not scanned, but listing them documents
          // intent and keeps the config correct if the mode is ever widened.
          callees: {
            exclude: [
              "i18n(ext)?",
              "t",
              "tc",
              "pick",
              "asString",
              "asNumber",
              "asInt",
              "asBool",
              "asCode",
              "asSource",
              "cn",
              "clsx",
              "twMerge",
              "cva",
              "require",
              "addEventListener",
              "removeEventListener",
              "postMessage",
              "getElementById",
              "dispatch",
              "commit",
              "includes",
              "indexOf",
              "endsWith",
              "startsWith",
            ],
          },
          // `words.exclude` REPLACES the plugin defaults, so the default
          // punctuation/constant patterns are restated, plus:
          //  - URL / route paths ("/docs", "/mcp") — identifiers, not copy.
          //  - placeholder / symbol-only text (em dash, ·, Δ, →, ×, …) used
          //    as glyphs around interpolated values, not translatable words.
          words: {
            exclude: [
              "[0-9!-/:-@[-`{-~]+",
              "[A-Z_-]+",
              "\\/.*",
              "^[\\s\\u2014\\u00b7\\u2022\\u221e\\u0394\\u2192\\u00d7=\\u2026-]+$",
            ],
          },
        },
      ],
    },
  },
);
