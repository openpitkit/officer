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

import type { ReactNode } from "react";

import { Card } from "@/components/ui/card";

/** A selectable catalog item with separately actionable reference links. */
export function SelectionTile({
  title,
  description,
  icon,
  onSelect,
  children,
}: {
  title: string;
  description?: string;
  icon?: ReactNode;
  onSelect: () => void;
  children?: ReactNode;
}) {
  return (
    <Card className="flex flex-col overflow-hidden">
      <button
        type="button"
        className="flex w-full flex-1 items-start p-4 text-left transition-colors hover:bg-surface-hover"
        onClick={onSelect}
      >
        <div className="flex items-start gap-3">
          {icon}
          <div className="min-w-0">
            <p className="font-semibold text-text">{title}</p>
            {description !== undefined && (
              <p className="mt-1 text-xs text-muted">{description}</p>
            )}
          </div>
        </div>
      </button>
      {children}
    </Card>
  );
}
