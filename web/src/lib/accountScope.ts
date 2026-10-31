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
// Please see https://officer.openpit.dev and the OWNERS file for details.

import type { AccountFeatures } from "@/api/accountMode";

/** Whether the panel shows accounts, and which account a request carries. */
export interface AccountScope {
  /** Whether accounts and account groups are shown (`features.accounts`). */
  readonly showsAccounts: boolean;
  /**
   * The default account the running process acts on (`features.account`);
   * empty while accounts are shown.
   */
  readonly account: string;
  /**
   * The account a request carries: `chosen` while accounts are shown, the
   * default account otherwise.
   */
  accountFor(chosen: string): string;
}

let current: AccountScope | null = null;

/** How many holds keep a changed scope from reloading the page. */
let reloadHolds = 0;

/** Store the running process's features as this page load's account scope. */
export function setAccountScope(features: AccountFeatures): void {
  const { accounts, account } = features;
  current = Object.freeze({
    showsAccounts: accounts,
    account,
    accountFor: (chosen: string) => (accounts ? chosen : account),
  });
}

/**
 * The account scope of this page load.
 *
 * A plain module snapshot rather than React state: it never changes while the
 * page lives (the running process enforces another mode only after a restart,
 * or, before the first-run setup is complete, after a save it takes at once;
 * either reloads the page), and predicates evaluated outside React read it
 * too.
 *
 * @throws Error when read before the scope is loaded.
 */
export function accountScope(): AccountScope {
  if (current === null) {
    throw new Error("account scope is not loaded");
  }
  return current;
}

/**
 * Whether `features` are the ones this page load's scope was set from: other
 * ones need the page reloaded.
 *
 * @throws Error when read before the scope is loaded.
 */
export function isLoadedScope(features: AccountFeatures): boolean {
  const { showsAccounts, account } = accountScope();
  return features.accounts === showsAccounts && features.account === account;
}

/**
 * Runs `work` while the page reload a changed scope triggers is held: for a
 * screen that changes the scope itself and reloads the page once its own
 * follow-up is done. The hold ends when `work` settles.
 */
export async function holdingScopeReload<T>(
  work: () => Promise<T>,
): Promise<T> {
  reloadHolds += 1;
  try {
    return await work();
  } finally {
    reloadHolds -= 1;
  }
}

/** Whether a screen holds the reload a changed scope triggers. */
export function scopeReloadHeld(): boolean {
  return reloadHolds > 0;
}
