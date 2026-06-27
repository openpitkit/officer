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

import type { ComponentType } from "react";

import type { Permission } from "../auth/auth-context";

/**
 * Routes carry their default Component. Page entries override the component
 * rendered for that route id; re-registering the same id replaces the override,
 * and unregistering removes it so rendering falls back to the route default.
 * Closed apps hide pages by unregistering route/nav ids or gating with when,
 * replace pages here, and remove structure with the route/nav unregister APIs.
 */
export interface PageEntry {
  id: string;
  titleKey?: string;
  Component: ComponentType;
  permission?: Permission;
  when?: () => boolean;
}

const pages = new Map<string, PageEntry>();

export function registerPage(entry: PageEntry): void {
  pages.set(entry.id, entry);
}

export function unregisterPage(id: string): void {
  pages.delete(id);
}

export function getPage(id: string): PageEntry | undefined {
  return pages.get(id);
}

export function resetPages(): void {
  pages.clear();
}
