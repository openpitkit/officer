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

import type { AccessMode } from "@/api/access";

/** The access mode the panel runs under. */
export interface PanelSession {
  /** `open` admits everyone; `token` runs on a password session. */
  readonly accessMode: AccessMode;
}

let current: PanelSession | null = null;
const sessionChecks = new Set<() => void>();

/** Store the access mode the startup gate admitted this browser under. */
export function setPanelSession(accessMode: AccessMode): void {
  current = Object.freeze({ accessMode });
}

/**
 * The access mode the startup gate admitted this browser under.
 *
 * A plain module snapshot rather than React state, so predicates evaluated
 * outside React read it too.
 *
 * @throws Error when read before the startup gate set it.
 */
export function panelSession(): PanelSession {
  if (current === null) {
    throw new Error("panel session is not loaded");
  }
  return current;
}

/**
 * Ask the startup gate to check the session again: it shows the login screen
 * when the browser no longer has one.
 */
export function checkSession(): void {
  for (const check of sessionChecks) {
    check();
  }
}

/**
 * Call `check` on every `checkSession()` until the returned function
 * unsubscribes it.
 */
export function onCheckSession(check: () => void): () => void {
  sessionChecks.add(check);
  return () => {
    sessionChecks.delete(check);
  };
}
