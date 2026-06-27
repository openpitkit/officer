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

import type { Permission } from "../auth/auth-context";
import { createOrderedRegistry } from "./store";

/**
 * Row actions are open-ended by kind. Registering a new id adds, registering an
 * existing id replaces, and unregistering removes the action structurally.
 * The optional when predicate is render-time visibility only.
 */
export interface RowActionEntry<Row = unknown, Ctx = unknown> {
  id: string;
  kind: string;
  order: number;
  permission?: Permission;
  when?: (row: Row, ctx: Ctx) => boolean;
  render: (row: Row, ctx: Ctx) => ReactNode;
}

const rowActions = createOrderedRegistry<RowActionEntry>();

export function registerRowAction<Row, Ctx>(
  entry: RowActionEntry<Row, Ctx>,
): void {
  rowActions.register(entry as RowActionEntry);
}

export function unregisterRowAction(id: string): void {
  rowActions.unregister(id);
}

export function getRowActions(kind: string): readonly RowActionEntry[] {
  return rowActions.get().filter((entry) => entry.kind === kind);
}

export function resetRowActions(): void {
  rowActions.reset();
}
