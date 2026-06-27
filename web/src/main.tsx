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

import { StrictMode, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter } from "react-router-dom";

import App from "@/App";
import { ApiClientProvider, AuthProvider } from "@/framework";
import i18n from "@/i18n";
import { LocaleProvider } from "@/i18n/LocaleProvider";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";
import "@/index.css";
import "@/register";

const container = document.getElementById("root");
if (!container) {
  throw new Error("root element #root not found");
}

const apiConfig = {
  baseUrl: "/app/api/v1",
  translate: (key: string, params?: Record<string, unknown>) =>
    i18n.t(key, params),
};

// Resources are bundled eagerly (see i18n/index.ts glob), so i18next is ready
// synchronously and Suspense only guards the edge case of a not-yet-ready tree.
createRoot(container).render(
  <StrictMode>
    <ThemeProvider storageKey="pit-officer-theme" defaultMode="system">
      <DisplayPreferencesProvider>
        <LocaleProvider>
          <Suspense fallback={null}>
            <ApiClientProvider config={apiConfig}>
              <BrowserRouter>
                <AuthProvider>
                  <App />
                </AuthProvider>
              </BrowserRouter>
            </ApiClientProvider>
          </Suspense>
        </LocaleProvider>
      </DisplayPreferencesProvider>
    </ThemeProvider>
  </StrictMode>,
);
