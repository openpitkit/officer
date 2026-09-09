// Copyright The Pit Project Owners. All rights reserved.
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Please see https://openpit.dev and the OWNERS file for details.

import i18n, { type Resource, type ResourceLanguage } from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import { initReactI18next } from "react-i18next";

import {
  DEFAULT_LOCALE,
  getSupportedLocaleCodes,
  registerLocale,
  resetLocales,
  unregisterLocale,
} from "./locales";

// Storage key the detected/selected language is cached under. The detector
// mirrors the value to localStorage and cookie, so preferences survive a
// service restart even if the UI is later served from a different port.
const STORAGE_KEY = "pit-officer-lang";

// Resource catalogs are discovered by glob: every locales/<locale>/<ns>.json
// is loaded eagerly and folded into { [locale]: { [namespace]: json } }.
// Adding a namespace or language file needs no edit here.
type Catalog = { default: ResourceLanguage };
const modules = import.meta.glob<Catalog>("./locales/**/*.json", {
  eager: true,
});

export const appLocaleResources: Resource = {};
for (const [path, mod] of Object.entries(modules)) {
  // Path shape: ./locales/<locale>/<namespace>.json
  const match = path.match(/\.\/locales\/([^/]+)\/([^/]+)\.json$/);
  if (!match) {
    continue;
  }
  const [, locale, namespace] = match;
  (appLocaleResources[locale] ??= {})[namespace] = mod.default;
}

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: appLocaleResources,
    fallbackLng: DEFAULT_LOCALE,
    supportedLngs: getSupportedLocaleCodes(),
    // A detected language outside the supported set falls back to en rather
    // than narrowing a region tag (e.g. en-GB) to a missing base.
    nonExplicitSupportedLngs: false,
    defaultNS: "common",
    detection: {
      order: ["localStorage", "cookie", "navigator"],
      caches: ["localStorage", "cookie"],
      lookupLocalStorage: STORAGE_KEY,
      lookupCookie: STORAGE_KEY,
    },
    interpolation: {
      // React already escapes interpolated values against XSS.
      escapeValue: false,
    },
  });

/** Register or replace a locale namespace resource bundle. */
export function registerLocaleResources(
  locale: string,
  namespace: string,
  catalog: ResourceLanguage,
): void {
  i18n.addResourceBundle(locale, namespace, catalog, true, true);
}

/** Register locale namespace resource bundles from an i18next resource map. */
export function registerLocaleResourceMap(resources: Resource): void {
  for (const [locale, namespaces] of Object.entries(resources)) {
    for (const [namespace, catalog] of Object.entries(namespaces ?? {})) {
      registerLocaleResources(locale, namespace, catalog as ResourceLanguage);
    }
  }
}

export { registerLocale, resetLocales, unregisterLocale };
export default i18n;
