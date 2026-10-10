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

import { Badge } from "@/components/ui/badge";
import { DashboardNote } from "./DashboardNote";

export interface DashboardItem {
  key: string;
  text: string;
}

export interface DashboardItemChipsProps {
  /** Items in display order. */
  items: readonly DashboardItem[];
  variant: "ok" | "neutral";
  /** How many items are shown as chips. */
  limit: number;
  /** Text for the count of items beyond the limit. */
  moreLabel: (count: number) => string;
}

/** Item chips cut to a limit, with the count of the rest. */
export function DashboardItemChips({
  items,
  variant,
  limit,
  moreLabel,
}: DashboardItemChipsProps) {
  const shown = items.slice(0, limit);
  const extra = items.length - shown.length;
  return (
    <>
      {shown.map((item) => (
        <Badge key={item.key} variant={variant}>
          {item.text}
        </Badge>
      ))}
      {extra > 0 && <DashboardNote>{moreLabel(extra)}</DashboardNote>}
    </>
  );
}
