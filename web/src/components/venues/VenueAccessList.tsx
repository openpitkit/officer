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

import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";

import type { TradingAccessGrant, TradingAccessSubject } from "@/api/types";
import {
  AutocompleteFilterField,
  DeleteButton,
  FieldLabel,
  FilterBar,
  RowActions,
  Segmented,
} from "@/components/data";
import { PageSizeSelect, TablePagination } from "@/components/TableControls";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { knownPageCount, slicePage } from "@/lib/tablePagination";
import { usePersistentPageSize } from "@/lib/tablePageSize";

/** One page size for every connection's access list. */
const PAGE_SIZE_STORAGE_KEY = "pit-officer-venue-access-page-size";

type GranteeKind = "all" | "accounts" | "groups";

/** The account or group code a grant is for. */
function grantCode(grant: TradingAccessGrant): string {
  return grant.accountGroup !== "" ? grant.accountGroup : grant.account;
}

/** Whether a grant is for a subject of the chosen kind. */
function isOfKind(grant: TradingAccessGrant, kind: GranteeKind): boolean {
  return kind === "all" || (grant.accountGroup !== "") === (kind === "groups");
}

/** Grants of one connection plus the revoke request; the caller confirms and
 *  performs the revoke. */
export interface VenueAccessListProps {
  /** Every grant of the connection, in the order the server sends them. */
  grants: TradingAccessGrant[];
  /** Adds the venue-account column. */
  showVenueAccount: boolean;
  /** Disables revoking while a mutation is in flight. */
  busy: boolean;
  onRevoke: (subject: TradingAccessSubject) => void;
}

/** Counted, filterable, paged list of a connection's account and group
 *  grants. Filtering and paging run over the given grants. */
export function VenueAccessList({
  grants,
  showVenueAccount,
  busy,
  onRevoke,
}: VenueAccessListProps) {
  const { t } = useTranslation("venues");
  const { t: tc } = useTranslation();
  const [kind, setKind] = useState<GranteeKind>("all");
  const [code, setCode] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = usePersistentPageSize(PAGE_SIZE_STORAGE_KEY);

  const groupCount = grants.filter((grant) => grant.accountGroup !== "").length;
  const query = code.trim().toLowerCase();
  const filtered = grants.filter(
    (grant) =>
      isOfKind(grant, kind) && grantCode(grant).toLowerCase().includes(query),
  );
  // The code field suggests the codes of the chosen kind that start with the
  // typed text, matched case-insensitively and capped like every filter's;
  // the list keeps the grants whose code contains it. Memoized on the codes'
  // value: every poll brings a new grant list, and a new suggestion list
  // resets the field's highlighted suggestion.
  const codesKey = JSON.stringify(
    grants.filter((grant) => isOfKind(grant, kind)).map(grantCode),
  );
  const codeSuggestions = useMemo(
    () => JSON.parse(codesKey) as string[],
    [codesKey],
  );
  const pageCount = knownPageCount(filtered.length, pageSize);
  // A revoke can shrink the list under the open page: show the last one left.
  const currentPage = Math.min(page, pageCount - 1);
  const rows = slicePage(filtered, currentPage, pageSize);
  const toFirstPage = (change: () => void) => {
    change();
    setPage(0);
  };
  const pager = (
    <TablePagination
      page={currentPage}
      canPrevious={currentPage > 0}
      canNext={currentPage < pageCount - 1}
      knownTotalPages={pageCount}
      onPrevious={() => setPage(currentPage - 1)}
      onNext={() => setPage(currentPage + 1)}
      onPage={setPage}
    />
  );

  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted">
          {t("access.accountCount", { count: grants.length - groupCount })}
          {" · "}
          {t("access.groupCount", { count: groupCount })}
        </p>
        <PageSizeSelect
          value={pageSize}
          onChange={(value) => toFirstPage(() => setPageSize(value))}
          ariaLabel={t("pagination.pageSize.ariaLabel")}
          rowCountLabel={(count) =>
            t("pagination.pageSize.rowCount", { count })
          }
        />
      </div>
      <FilterBar
        active={kind !== "all" || query !== ""}
        activeLabel={tc("filters.active")}
        onClearActive={() =>
          toFirstPage(() => {
            setKind("all");
            setCode("");
          })
        }
        clearActiveLabel={tc("filters.clearAll")}
        trailing={
          <div className="grid gap-1.5">
            <FieldLabel>{t("access.kindFilter")}</FieldLabel>
            <Segmented
              value={kind}
              options={[
                { value: "all", label: t("access.kind.all") },
                { value: "accounts", label: t("access.kind.accounts") },
                { value: "groups", label: t("access.kind.groups") },
              ]}
              onChange={(value) =>
                toFirstPage(() => setKind(value as GranteeKind))
              }
            />
          </div>
        }
      >
        <AutocompleteFilterField
          label={t("access.codeFilter")}
          value={code}
          placeholder={t("access.codePlaceholder")}
          suggestions={codeSuggestions}
          onChange={(value) => toFirstPage(() => setCode(value))}
          onClear={() => toFirstPage(() => setCode(""))}
          clearLabel={tc("filters.clearField")}
          width={220}
        />
      </FilterBar>
      {pager}
      <Table aria-label={t("access.tableLabel")}>
        <TableHeader>
          <TableRow>
            <TableHead>{t("access.columns.subject")}</TableHead>
            <TableHead>{t("access.columns.group")}</TableHead>
            {showVenueAccount && <TableHead>{t("venueAccount")}</TableHead>}
            <TableHead className="text-right">{t("table.actions")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.length === 0 && (
            <TableRow>
              <TableCell
                className="py-5 text-center text-muted"
                colSpan={showVenueAccount ? 4 : 3}
              >
                {t(grants.length === 0 ? "access.empty" : "access.noMatches")}
              </TableCell>
            </TableRow>
          )}
          {rows.map((grant) => {
            const isGroup = grant.accountGroup !== "";
            const subjectCode = grantCode(grant);
            return (
              <TableRow key={`${isGroup ? "group" : "account"}:${subjectCode}`}>
                <TableCell className="font-medium">
                  <div className="flex items-center gap-2">
                    <span>{subjectCode}</span>
                    {isGroup && <Badge>{t("access.groupBadge")}</Badge>}
                  </div>
                </TableCell>
                <TableCell>
                  {grant.group !== "" && (
                    <Badge variant="accent" className="normal-case">
                      {grant.group}
                    </Badge>
                  )}
                </TableCell>
                {showVenueAccount && (
                  <TableCell>{grant.venueAccount}</TableCell>
                )}
                <TableCell className="text-right">
                  <RowActions>
                    <DeleteButton
                      disabled={busy}
                      title={t(
                        isGroup
                          ? "actions.revokeGroupAccessFor"
                          : "actions.revokeAccountAccessFor",
                        { code: subjectCode },
                      )}
                      onClick={() =>
                        onRevoke(
                          isGroup
                            ? { accountGroup: subjectCode }
                            : { account: subjectCode },
                        )
                      }
                    />
                  </RowActions>
                </TableCell>
              </TableRow>
            );
          })}
        </TableBody>
      </Table>
      {pager}
    </div>
  );
}
