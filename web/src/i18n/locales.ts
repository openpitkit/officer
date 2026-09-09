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

import i18n from "i18next";

/** A supported UI locale. */
export interface Locale {
  /** BCP-47 code, also the resource and detector key. */
  code: string;
  /** The language's own native name. Never translated into another UI
   *  language: a user whose language was mis-detected must still recognize
   *  and reach their own entry. */
  endonym: string;
}

/** The supported locales, in menu order. Adding a language is a one-line
 *  edit here plus a `locales/<code>/` catalog folder; nothing else changes. */
export const LOCALES: readonly Locale[] = [
  { code: "en", endonym: "English" },
  { code: "ru", endonym: "Русский" },
  { code: "zh-CN", endonym: "中文" },
];

/** The locale codes, derived from {@link LOCALES} for i18next supportedLngs. */
export const LOCALE_CODES: readonly string[] = LOCALES.map((l) => l.code);

/** The default locale, used as the fallback and when detection misses. */
export const DEFAULT_LOCALE = "en";

function defaultSupportedLocales(): Locale[] {
  return LOCALES.map((locale) => ({ ...locale }));
}

const supportedLocales: Locale[] = defaultSupportedLocales();

function refreshSupportedLanguages(): void {
  i18n.options.supportedLngs = [...getSupportedLocaleCodes()];
}

/** Return the currently supported locales, including registered extensions. */
export function getSupportedLocales(): readonly Locale[] {
  return supportedLocales.map((locale) => ({ ...locale }));
}

/** Return supported locale codes, including registered extensions. */
export function getSupportedLocaleCodes(): readonly string[] {
  return supportedLocales.map((locale) => locale.code);
}

/** Register an additional locale for external resource bundles. */
export function registerLocale(locale: Locale): void {
  const existing = supportedLocales.find((entry) => entry.code === locale.code);
  if (existing) {
    existing.endonym = locale.endonym;
  } else {
    supportedLocales.push({ ...locale });
  }

  refreshSupportedLanguages();
}

/** Remove an extension locale or reset an open app locale override. */
export function unregisterLocale(code: string): boolean {
  const defaultLocale = LOCALES.find((entry) => entry.code === code);
  const index = supportedLocales.findIndex((entry) => entry.code === code);
  if (index < 0) {
    return false;
  }
  if (defaultLocale) {
    supportedLocales[index] = { ...defaultLocale };
    refreshSupportedLanguages();
    return true;
  }
  supportedLocales.splice(index, 1);
  refreshSupportedLanguages();
  return true;
}

/** Reset runtime-supported locales to the application defaults. */
export function resetLocales(): void {
  supportedLocales.splice(
    0,
    supportedLocales.length,
    ...defaultSupportedLocales(),
  );
  refreshSupportedLanguages();
}
