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

import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";

import type { Permission } from "../auth/auth-context";
import { createOrderedRegistry } from "./store";

export type NavSection = "primary" | "footer";

/**
 * Navigation entries support add, replace, and structural removal by id.
 * The optional when predicate only gates per-render visibility; unregistering
 * remains the structural remove API.
 */
export interface NavEntry {
  id: string;
  to: string;
  labelKey: string;
  icon: LucideIcon;
  section: NavSection;
  order: number;
  end?: boolean;
  permission?: Permission;
  when?: () => boolean;
  render?: () => ReactNode;
}

const nav = createOrderedRegistry<NavEntry>();

export function registerNav(entry: NavEntry): void {
  nav.register(entry);
}

export function unregisterNav(id: string): void {
  nav.unregister(id);
}

export function getNav(section: NavSection): readonly NavEntry[] {
  return nav.get().filter((entry) => entry.section === section);
}

export function resetNav(): void {
  nav.reset();
}
