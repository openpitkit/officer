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

// Theme types, context, and hook, kept apart from the provider component so
// the provider file exports only a component (Fast Refresh).

import { createContext, useContext } from "react";

/**
 * The user-selected theme preference. "system" follows prefers-color-scheme.
 */
export type ThemeMode = "dark" | "light" | "system";

/** The concrete palette actually applied to the document. */
export type ResolvedTheme = "dark" | "light";

export interface ThemeContextValue {
  /** The persisted preference: dark, light, or system. */
  mode: ThemeMode;
  /** The palette currently applied, with "system" already resolved. */
  resolved: ResolvedTheme;
  /** Update and persist the preference. */
  setMode: (mode: ThemeMode) => void;
}

export const ThemeContext = createContext<ThemeContextValue | null>(null);

/** Access the current theme preference and switcher. */
export function useTheme(): ThemeContextValue {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error("useTheme must be used within a ThemeProvider");
  }
  return ctx;
}
