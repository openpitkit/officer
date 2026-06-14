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

import { useEffect, type ReactNode } from "react";
import { useTranslation } from "react-i18next";

// Keeps <html lang> in sync with the active i18next language. The detector
// owns detection and browser persistence (see i18n/index.ts); this only mirrors
// the resolved language onto the document, the same way ThemeProvider mirrors
// the resolved palette. Mounting `@/i18n` initializes i18next, so this provider
// just observes and applies.
export function LocaleProvider({ children }: { children: ReactNode }) {
  const { i18n } = useTranslation();

  useEffect(() => {
    const apply = (lng: string) => {
      document.documentElement.lang = lng;
    };
    apply(i18n.language);
    i18n.on("languageChanged", apply);
    return () => i18n.off("languageChanged", apply);
  }, [i18n]);

  return <>{children}</>;
}
