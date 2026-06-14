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
  DisplayPreferencesContext,
  type DensityMode,
  type DisplayPreferencesContextValue,
  type TradeStyle,
} from "@/theme/display-context";
import {
  readStoredPreference,
  writeStoredPreference,
} from "@/lib/browserStorage";

function isDensityMode(value: string | null): value is DensityMode {
  return (
    value === "comfortable" ||
    value === "compact" ||
    value === "terminal"
  );
}

function isTradeStyle(value: string | null): value is TradeStyle {
  return value === "tape" || value === "ladder";
}

function readStored<T extends string>(
  storageKey: string,
  fallback: T,
  guard: (value: string | null) => value is T,
): T {
  if (typeof window === "undefined") {
    return fallback;
  }
  const stored = readStoredPreference(storageKey);
  if (guard(stored)) {
    return stored;
  }
  return fallback;
}

function persist(storageKey: string, value: string): void {
  writeStoredPreference(storageKey, value);
}

interface DisplayPreferencesProviderProps {
  children: ReactNode;
  densityStorageKey?: string;
  tradeStyleStorageKey?: string;
  defaultDensity?: DensityMode;
  defaultTradeStyle?: TradeStyle;
}

export function DisplayPreferencesProvider({
  children,
  densityStorageKey = "pit-officer-density",
  tradeStyleStorageKey = "pit-officer-trade-style",
  defaultDensity = "compact",
  defaultTradeStyle = "tape",
}: DisplayPreferencesProviderProps) {
  const [density, setDensityState] = useState<DensityMode>(() =>
    readStored(densityStorageKey, defaultDensity, isDensityMode),
  );
  const [tradeStyle, setTradeStyleState] = useState<TradeStyle>(() =>
    readStored(tradeStyleStorageKey, defaultTradeStyle, isTradeStyle),
  );

  useEffect(() => {
    const root = document.documentElement;
    root.dataset.density = density;
    root.dataset.tradeStyle = tradeStyle;
  }, [density, tradeStyle]);

  const setDensity = useCallback(
    (next: DensityMode) => {
      setDensityState(next);
      persist(densityStorageKey, next);
    },
    [densityStorageKey],
  );

  const setTradeStyle = useCallback(
    (next: TradeStyle) => {
      setTradeStyleState(next);
      persist(tradeStyleStorageKey, next);
    },
    [tradeStyleStorageKey],
  );

  const value = useMemo<DisplayPreferencesContextValue>(
    () => ({ density, tradeStyle, setDensity, setTradeStyle }),
    [density, tradeStyle, setDensity, setTradeStyle],
  );

  return (
    <DisplayPreferencesContext.Provider value={value}>
      {children}
    </DisplayPreferencesContext.Provider>
  );
}
