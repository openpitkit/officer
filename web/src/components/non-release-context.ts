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

// Context and hook for the non-release banner, kept apart from the gate
// component so the gate file exports only components (Fast Refresh).

import { createContext, useContext } from "react";

export interface NonReleaseState {
  /** Whether this build is a non-release build. */
  nonRelease: boolean;
  /**
   * Tells the gate that the sidebar shows the banner, so that the gate keeps
   * its top strip only where the sidebar is off screen.
   */
  showInSidebar: (shown: boolean) => void;
}

export const NonReleaseContext = createContext<NonReleaseState | null>(null);

export function useNonRelease(): NonReleaseState {
  const ctx = useContext(NonReleaseContext);
  if (!ctx) throw new Error("useNonRelease must be used inside NonReleaseGate");
  return ctx;
}
