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

import { useEffect, useState } from "react";

import { AUTOCOMPLETE_SUGGESTION_LIMIT } from "@/framework/constants";
import {
  DEFAULT_SEARCH_DEBOUNCE_MS,
  useDebouncedValue,
} from "@/lib/useDebounce";

function sameStrings(left: string[], right: string[]): boolean {
  return (
    left.length === right.length &&
    left.every((value, index) => value === right[index])
  );
}

/** Code suggestions for the debounced query: once the trimmed query is
 *  non-empty and `enabled` holds, `list` is asked for the first
 *  AUTOCOMPLETE_SUGGESTION_LIMIT items, by code, whose code starts with it.
 *  `failed` is true after a lookup error, until the next lookup succeeds.
 *  `list` must keep its identity across renders, as the bound OfficerApi list
 *  functions do: a new function starts a new lookup. */
export function useCodeSuggestions(
  query: string,
  enabled: boolean,
  list: (
    filters: {
      code: string;
      codeMatch: "starts_with";
      limit: number;
      sort: "code";
    },
    signal: AbortSignal,
  ) => Promise<{ code: string }[]>,
): { codes: string[]; failed: boolean } {
  const debouncedQuery = useDebouncedValue(query, DEFAULT_SEARCH_DEBOUNCE_MS);
  const trimmed = debouncedQuery.trim();
  const canFetch = enabled && trimmed !== "";
  const [codes, setCodes] = useState<string[]>([]);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    if (!canFetch) {
      return;
    }
    const controller = new AbortController();
    void list(
      {
        code: trimmed,
        codeMatch: "starts_with",
        limit: AUTOCOMPLETE_SUGGESTION_LIMIT,
        sort: "code",
      },
      controller.signal,
    )
      .then((items) => {
        const next = items.map((item) => item.code);
        setCodes((previous) => (sameStrings(previous, next) ? previous : next));
        setFailed(false);
      })
      .catch((error: unknown) => {
        if (!controller.signal.aborted) {
          console.error(error);
          setCodes((previous) => (previous.length === 0 ? previous : []));
          setFailed(true);
        }
      });
    return () => controller.abort();
  }, [canFetch, list, trimmed]);

  return canFetch ? { codes, failed } : { codes: [], failed: false };
}
