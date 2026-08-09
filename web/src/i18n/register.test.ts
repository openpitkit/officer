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

import { describe, expect, it } from "vitest";

import i18n, { registerLocaleResources } from "@/i18n";
import {
  getSupportedLocaleCodes,
  getSupportedLocales,
  registerLocale,
  resetLocales,
  unregisterLocale,
} from "@/i18n/locales";

describe("i18n registration", () => {
  it("resolves keys from registered namespace resources", () => {
    registerLocaleResources("en", "authSmoke", {
      title: "Registered auth smoke",
    });

    expect(i18n.t("authSmoke:title")).toBe("Registered auth smoke");
  });

  it("resets and unregisters runtime locale registrations", () => {
    resetLocales();

    registerLocale({ code: "es", endonym: "Spanish" });
    expect(getSupportedLocaleCodes()).toContain("es");
    expect(unregisterLocale("es")).toBe(true);
    expect(getSupportedLocaleCodes()).not.toContain("es");

    registerLocale({ code: "en", endonym: "Custom English" });
    expect(unregisterLocale("en")).toBe(true);
    const english = getSupportedLocales().find(
      (locale) => locale.code === "en",
    );
    expect(english).toEqual({ code: "en", endonym: "English" });

    registerLocale({ code: "de", endonym: "Deutsch" });
    resetLocales();
    expect(getSupportedLocaleCodes()).not.toContain("de");
  });

  it("returns locale snapshots instead of the mutable registry", () => {
    resetLocales();

    const locales = getSupportedLocales() as {
      code: string;
      endonym: string;
    }[];
    locales.push({ code: "it", endonym: "Italiano" });

    expect(getSupportedLocaleCodes()).not.toContain("it");
  });
});
