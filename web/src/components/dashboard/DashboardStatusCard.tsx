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

import { Loader2 } from "lucide-react";
import type { ReactNode } from "react";
import { useNavigate } from "react-router-dom";

import { CompactStaleState } from "@/components/PageStates";
import { Card, CardContent, CardTitle } from "@/components/ui/card";
// Relative, so the published declaration of the props resolves outside this
// repository, where the "@/" alias does not exist.
import type { LoadState } from "../../api/usePolling";
import { DashboardNote } from "./DashboardNote";

export interface DashboardStatusCardProps<T> {
  title: string;
  /** Route the whole card opens. */
  to: string;
  /** Polled source; the card draws its loading, error and stale states. */
  load: LoadState<T>;
  /** Content for loaded data, placed after the title on one wrapping line. */
  children: (data: T) => ReactNode;
}

/** One-line dashboard summary of a polled source that opens its page. */
export function DashboardStatusCard<T>({
  title,
  to,
  load,
  children,
}: DashboardStatusCardProps<T>) {
  const navigate = useNavigate();
  return (
    <button
      type="button"
      onClick={() => navigate(to)}
      className="animate-fade-in w-full cursor-pointer rounded-card text-left hover:bg-accent-dim focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <Card className="pointer-events-none">
        <CardContent className="flex flex-wrap items-center gap-2 py-3">
          <CardTitle className="shrink-0">{title}</CardTitle>
          {load.state === "loading" && (
            <Loader2 className="h-3.5 w-3.5 animate-spin text-muted" />
          )}
          <CompactStaleState load={load} />
          {load.state === "error" && (
            <DashboardNote>{load.error}</DashboardNote>
          )}
          {load.state === "ready" && children(load.data)}
        </CardContent>
      </Card>
    </button>
  );
}
