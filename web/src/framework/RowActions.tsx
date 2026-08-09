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

import { Fragment } from "react";

import { useHasPermission } from "./auth/auth-context";
import { getRowActions, type RowActionEntry } from "./registries/actions";

export function RowActions<Row, Ctx>({
  kind,
  row,
  ctx,
}: {
  kind: string;
  row: Row;
  ctx: Ctx;
}) {
  const hasPermission = useHasPermission();

  return (
    <>
      {getRowActions(kind).map((entry) => {
        const typedEntry = entry as RowActionEntry<Row, Ctx>;
        if (typedEntry.permission && !hasPermission(typedEntry.permission)) {
          return null;
        }
        if (!(typedEntry.when?.(row, ctx) ?? true)) {
          return null;
        }
        return (
          <Fragment key={entry.id}>{typedEntry.render(row, ctx)}</Fragment>
        );
      })}
    </>
  );
}
