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

import { useMemo, useSyncExternalStore } from "react";

import {
  readStoredPreference,
  writeStoredPreference,
} from "@/lib/browserStorage";

const STORAGE_KEY = "pit-officer-global-account-filter";

const listeners = new Set<() => void>();

let currentAccount = readStoredAccount();

function readStoredAccount(): string {
  return (readStoredPreference(STORAGE_KEY) ?? "").trim();
}

function notify(): void {
  for (const listener of listeners) {
    listener();
  }
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

function getSnapshot(): string {
  return currentAccount;
}

function getServerSnapshot(): string {
  return "";
}

export function setGlobalAccountFilter(account: string): void {
  const next = account.trim();
  if (next === currentAccount) {
    return;
  }
  currentAccount = next;
  writeStoredPreference(STORAGE_KEY, next);
  notify();
}

export function useGlobalAccountFilter() {
  const account = useSyncExternalStore(
    subscribe,
    getSnapshot,
    getServerSnapshot,
  );

  return useMemo(
    () => ({
      account,
      enabled: account !== "",
      setAccount: setGlobalAccountFilter,
      clear: () => setGlobalAccountFilter(""),
    }),
    [account],
  );
}
