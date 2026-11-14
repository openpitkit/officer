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

import { CircleAlert, Users } from "lucide-react";
import { useTranslation } from "react-i18next";

import type { TradingAccessSubject, TradingSnapshot } from "@/api/types";
import { VenueAccessButton } from "@/components/data/RowActions";

/** Effective access includes disabled connections and counts each connection once. */
function accountVenueAccess(
  snapshot: TradingSnapshot,
  subject: TradingAccessSubject,
  group: string | null,
) {
  let paper = false;
  let live = false;
  let inherited = false;
  const connections = new Set<string>();
  for (const connection of snapshot.connections) {
    const own = connection.access.some((grant) =>
      subject.accountGroup !== undefined
        ? grant.accountGroup === subject.accountGroup
        : grant.account === subject.account,
    );
    const fromGroup =
      group !== null &&
      group !== "" &&
      connection.access.some((grant) => grant.accountGroup === group);
    if (!own && !fromGroup) continue;
    connections.add(connection.id);
    inherited ||= !own && fromGroup;
    paper ||= connection.mode === "paper";
    live ||= connection.mode === "live";
  }
  return { count: connections.size, mixed: paper && live, inherited };
}

/** Dialog warning uses the same effective-access summary as the account cell. */
export function AccountTradingAccessWarning({
  snapshot,
  subject,
  group,
}: {
  snapshot: TradingSnapshot;
  subject: TradingAccessSubject;
  group: string | null;
}) {
  const { t } = useTranslation("accounts");
  if (
    (subject.account !== undefined && group === null) ||
    !accountVenueAccess(snapshot, subject, group).mixed
  )
    return null;
  return (
    <div className="flex items-center gap-2 text-sm">
      <MixedTradingAccessWarning />
      {t("mixedTradingAccess")}
    </div>
  );
}

/** Current access warning, independent of connection health and enabled state. */
export function MixedTradingAccessWarning() {
  const { t } = useTranslation("accounts");
  const warning = t("mixedTradingAccess");
  return (
    <span
      role="note"
      tabIndex={0}
      title={warning}
      aria-label={warning}
      className="inline-flex shrink-0 text-[var(--warn)]"
    >
      <CircleAlert size={14} aria-hidden="true" />
    </span>
  );
}

/** Shared venue cell for an account's list row and account screen. */
export function AccountVenueAccess({
  account,
  group,
  snapshot,
  onOpen,
}: {
  account: string;
  group: string;
  snapshot: TradingSnapshot | null;
  onOpen: () => void;
}) {
  const { t } = useTranslation("accounts");
  const summary =
    snapshot === null ? null : accountVenueAccess(snapshot, { account }, group);
  return (
    <div className="flex items-center gap-1">
      <span className="nums text-xs text-muted-lt">
        {summary === null ? "-" : summary.count}
      </span>
      {summary?.inherited && (
        <span
          title={t("inheritedVenueAccess")}
          aria-label={t("inheritedVenueAccess")}
        >
          <Users size={12} aria-hidden="true" />
        </span>
      )}
      {summary?.mixed && <MixedTradingAccessWarning />}
      <span className="ml-auto flex shrink-0">
        <VenueAccessButton title={t("venueAccessTitle")} onClick={onOpen} />
      </span>
    </div>
  );
}
