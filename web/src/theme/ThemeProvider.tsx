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

import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";

import {
  ThemeContext,
  type ResolvedTheme,
  type ThemeContextValue,
  type ThemeMode,
} from "@/theme/theme-context";

const MQ_DARK = "(prefers-color-scheme: dark)";

function isThemeMode(value: string | null): value is ThemeMode {
  return value === "dark" || value === "light" || value === "system";
}

function systemPrefersDark(): boolean {
  return (
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia(MQ_DARK).matches
  );
}

function resolve(mode: ThemeMode): ResolvedTheme {
  if (mode === "system") {
    return systemPrefersDark() ? "dark" : "light";
  }
  return mode;
}

function applyToDocument(resolved: ResolvedTheme): void {
  const root = document.documentElement;
  root.classList.remove("dark", "light");
  root.classList.add(resolved);
  root.style.colorScheme = resolved;
}

interface ThemeProviderProps {
  children: ReactNode;
  /** localStorage key the preference is persisted under. */
  storageKey?: string;
  /** Preference used when nothing is persisted yet. */
  defaultMode?: ThemeMode;
}

export function ThemeProvider({
  children,
  storageKey = "pit-officer-theme",
  defaultMode = "system",
}: ThemeProviderProps) {
  const [mode, setModeState] = useState<ThemeMode>(() => {
    if (typeof window === "undefined") {
      return defaultMode;
    }
    try {
      const stored = window.localStorage.getItem(storageKey);
      if (isThemeMode(stored)) {
        return stored;
      }
    } catch {
      /* localStorage may be blocked (private mode); use the default. */
    }
    return defaultMode;
  });

  const [resolved, setResolved] = useState<ResolvedTheme>(() => resolve(mode));

  // Apply the resolved palette whenever the preference changes, and keep it in
  // sync with the OS when the preference is "system".
  useEffect(() => {
    const next = resolve(mode);
    // Re-resolve "system" against the live OS preference on each mode change;
    // the initializer only ran once. Intentional and behavior-critical.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    setResolved(next);
    applyToDocument(next);

    if (mode !== "system") {
      return;
    }
    const media = window.matchMedia(MQ_DARK);
    const onChange = () => {
      const r = media.matches ? "dark" : "light";
      setResolved(r);
      applyToDocument(r);
    };
    media.addEventListener("change", onChange);
    return () => media.removeEventListener("change", onChange);
  }, [mode]);

  const setMode = useCallback(
    (next: ThemeMode) => {
      setModeState(next);
      try {
        window.localStorage.setItem(storageKey, next);
      } catch {
        /* Persisting is best-effort; the in-memory preference still applies. */
      }
    },
    [storageKey],
  );

  const value = useMemo<ThemeContextValue>(
    () => ({ mode, resolved, setMode }),
    [mode, resolved, setMode],
  );

  return (
    <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
  );
}
