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

import { useState } from "react";

/** Search state and callbacks consumed by SymbolSearchPanel. */
export interface SymbolSearchState<Match> {
  query: string;
  results: Match[];
  filterQuery: string;
  searching: boolean;
  searched: boolean;
  onQueryChange: (query: string) => void;
  onFilterChange: (query: string) => void;
  onSearch: () => Promise<void>;
}

/**
 * Runs a trimmed nonempty query, clearing results and the filter on each run.
 * Query edits also clear the searched flag. Null is a caller-surfaced failure.
 */
export function useSymbolSearch<Match>(
  search: (query: string) => Promise<Match[] | null>,
): SymbolSearchState<Match> {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<Match[]>([]);
  const [filterQuery, setFilterQuery] = useState("");
  const [searching, setSearching] = useState(false);
  const [searched, setSearched] = useState(false);

  const onQueryChange = (value: string) => {
    setQuery(value);
    setResults([]);
    setFilterQuery("");
    setSearched(false);
  };
  const onSearch = async () => {
    const trimmedQuery = query.trim();
    if (trimmedQuery === "") return;
    setSearching(true);
    setSearched(true);
    setResults([]);
    setFilterQuery("");
    try {
      const matches = await search(trimmedQuery);
      if (matches !== null) setResults(matches);
    } finally {
      setSearching(false);
    }
  };

  return {
    query,
    results,
    filterQuery,
    searching,
    searched,
    onQueryChange,
    onFilterChange: setFilterQuery,
    onSearch,
  };
}
