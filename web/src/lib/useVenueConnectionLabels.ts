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

import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import type { TradingSnapshot } from "@/api/types";

/** Trading connection labels for venue columns and filters. */
export interface VenueConnectionLabels {
  /**
   * Connection ID to "venue · mode · name"; undefined while the snapshot is
   * not loaded or holds no connection, which hides venue columns and fields.
   */
  labels: ReadonlyMap<string, string> | undefined;
  /** The same connections in snapshot order, as `VenueFilter` options. */
  connections: { id: string; label: string }[];
}

/** Labels the snapshot's trading connections as "venue · mode · name". */
export function useVenueConnectionLabels(
  snapshot: TradingSnapshot | null,
): VenueConnectionLabels {
  const { t } = useTranslation("trading");
  return useMemo(() => {
    if (!snapshot || snapshot.connections.length === 0) {
      return { labels: undefined, connections: [] };
    }
    const connections = snapshot.connections.map((connection) => {
      const provider = snapshot.providers.find(
        (candidate) => candidate.type === connection.provider,
      );
      const venue = provider
        ? provider.title
        : t("unknownVenue", { provider: connection.provider });
      return {
        id: connection.id,
        label: `${venue} · ${t(`mode.${connection.mode}`)} · ${connection.label}`,
      };
    });
    return {
      labels: new Map(connections.map(({ id, label }) => [id, label])),
      connections,
    };
  }, [snapshot, t]);
}
