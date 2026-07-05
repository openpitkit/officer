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

import { cn } from "@/lib/utils";

export interface ColumnHeaderProps {
  children: ReactNode;
  description?: string;
  align?: "left" | "right";
}

export function ColumnHeader({
  children,
  description,
  align = "left",
}: ColumnHeaderProps) {
  return (
    <span
      title={description}
      className={cn(
        "inline-flex min-w-0 max-w-full items-center",
        align === "right" && "justify-end",
      )}
    >
      <span className="min-w-0 truncate">{children}</span>
    </span>
  );
}
