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

import { Badge } from "@/components/ui/badge";

export interface DashboardSourceRowProps {
  /** Source name, shown as the first chip. */
  label: string;
  /** Status chips, items and notes that follow the name. */
  children?: ReactNode;
}

/** One dashboard source on a wrapping line: its name, then its status. */
export function DashboardSourceRow({
  label,
  children,
}: DashboardSourceRowProps) {
  return (
    <span className="flex flex-wrap items-center gap-1">
      <Badge variant="neutral">{label}</Badge>
      {children}
    </span>
  );
}
