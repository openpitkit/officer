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

import { SearchCheck } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Button } from "@/components/ui/button";
import { ClearableInput } from "@/components/ClearableInput";
import { Label } from "@/components/ui/label";

/** Controlled symbol search and result picker, independent of match shape. */
export interface SymbolSearchPanelProps<Match> {
  id: string;
  label: string;
  placeholder: string;
  noResultsText: string;
  /** Optional muted venue capability notice inside the search panel. */
  notice?: string;
  busy: boolean;
  searching: boolean;
  searched: boolean;
  query: string;
  results: Match[];
  filterQuery: string;
  onQueryChange: (value: string) => void;
  onFilterChange: (value: string) => void;
  onSearch: () => void;
  onPick: (match: Match) => void;
  getKey: (match: Match) => string;
  getLabel: (match: Match) => string;
  getMetadata: (match: Match) => string;
  getName: (match: Match) => string | undefined;
  /** Warning rendered outside the truncated metadata line. */
  getWarning?: (match: Match) => string | undefined;
  /** Returns lower-cased text searched by the result filter. */
  getSearchText: (match: Match) => string;
}

/** Renders the compact search controls and scrollable matches. */
export function SymbolSearchPanel<Match>({
  id,
  label,
  placeholder,
  noResultsText,
  notice,
  busy,
  searching,
  searched,
  query,
  results,
  filterQuery,
  onQueryChange,
  onFilterChange,
  onSearch,
  onPick,
  getKey,
  getLabel,
  getMetadata,
  getName,
  getWarning,
  getSearchText,
}: SymbolSearchPanelProps<Match>) {
  const { t } = useTranslation("venues");
  const normalizedFilter = filterQuery.trim().toLowerCase();
  const filteredResults =
    normalizedFilter === ""
      ? results
      : results.filter((match) =>
          getSearchText(match).includes(normalizedFilter),
        );

  return (
    <div className="space-y-2 rounded-card border border-border bg-bg/50 p-3">
      <Label htmlFor={id}>{label}</Label>
      <div className="flex gap-2">
        <div className="min-w-0 flex-1">
          <ClearableInput
            id={id}
            value={query}
            onClear={() => onQueryChange("")}
            clearLabel={t("common:filters.clearField")}
            placeholder={placeholder}
            onChange={(e) => onQueryChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                onSearch();
              }
            }}
            disabled={busy || searching}
          />
        </div>
        <Button
          type="button"
          variant="outline"
          onClick={onSearch}
          disabled={busy || searching || query.trim() === ""}
        >
          <SearchCheck />
          {t("resolve.action")}
        </Button>
      </div>
      {notice && <p className="text-xs text-muted">{notice}</p>}
      {searching && (
        <p className="text-xs text-muted">{t("resolve.searching")}</p>
      )}
      {!searching && searched && results.length === 0 && (
        <p className="text-xs text-muted">{noResultsText}</p>
      )}
      {results.length > 0 && (
        <div className="space-y-2">
          <ClearableInput
            value={filterQuery}
            onClear={() => onFilterChange("")}
            clearLabel={t("common:filters.clearField")}
            placeholder={t("resolve.filterPlaceholder")}
            aria-label={t("resolve.filterLabel")}
            onChange={(e) => onFilterChange(e.target.value)}
            disabled={busy || searching}
          />
          {filteredResults.length === 0 ? (
            <p className="text-xs text-muted">
              {t("resolve.noFilteredResults")}
            </p>
          ) : (
            <ul className="max-h-52 divide-y divide-border overflow-y-auto rounded-card border border-border">
              {filteredResults.map((match) => {
                const metadata = getMetadata(match);
                const warning = getWarning?.(match);
                return (
                  <li key={getKey(match)}>
                    <button
                      type="button"
                      className="flex w-full items-center gap-3 px-3 py-2 text-left text-sm transition-colors hover:bg-surface-hover"
                      onClick={() => onPick(match)}
                      disabled={busy}
                    >
                      <span className="min-w-0">
                        <span className="block truncate text-text">
                          {getLabel(match)}
                          {metadata && (
                            <span className="text-muted">
                              {" · "}
                              {metadata}
                            </span>
                          )}
                        </span>
                        {getName(match) && (
                          <span className="mt-0.5 block truncate text-xs text-muted">
                            {getName(match)}
                          </span>
                        )}
                      </span>
                      {warning && (
                        <span className="shrink-0 text-xs text-[var(--warn)]">
                          {warning}
                        </span>
                      )}
                    </button>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
