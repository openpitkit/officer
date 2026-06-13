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

import { DEFAULT_LOCALE, LOCALE_CODES } from "@/i18n/locales";

// localStorage key the detected/selected language is cached under. The
// language switcher persists the manual choice through this same cache, so
// a manual pick overrides auto-detection on the next load.
const STORAGE_KEY = "pit-officer-lang";

// Resource catalogs are discovered by glob: every locales/<locale>/<ns>.json
// is loaded eagerly and folded into { [locale]: { [namespace]: json } }.
// Adding a namespace or language file needs no edit here.
type Catalog = { default: ResourceLanguage };
const modules = import.meta.glob<Catalog>("./locales/**/*.json", {
  eager: true,
});

const resources: Resource = {};
for (const [path, mod] of Object.entries(modules)) {
  // Path shape: ./locales/<locale>/<namespace>.json
  const match = path.match(/\.\/locales\/([^/]+)\/([^/]+)\.json$/);
  if (!match) {
    continue;
  }
  const [, locale, namespace] = match;
  (resources[locale] ??= {})[namespace] = mod.default;
}

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: DEFAULT_LOCALE,
    supportedLngs: LOCALE_CODES,
    // A detected language outside the supported set falls back to en rather
    // than narrowing a region tag (e.g. en-GB) to a missing base.
    nonExplicitSupportedLngs: false,
    defaultNS: "common",
    detection: {
      order: ["localStorage", "navigator"],
      caches: ["localStorage"],
      lookupLocalStorage: STORAGE_KEY,
    },
    interpolation: {
      // React already escapes interpolated values against XSS.
      escapeValue: false,
    },
  });

export default i18n;
