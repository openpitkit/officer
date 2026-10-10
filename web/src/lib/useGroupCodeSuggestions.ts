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

import { useOfficerApi } from "@/framework";
import { useCodeSuggestions } from "@/lib/useCodeSuggestions";

/** Account group codes starting with the debounced query. `failed` is true
 *  after a lookup error, until the next lookup succeeds. The query is trimmed
 *  before the debounce, so a whitespace-only edit does not restart it. */
export function useGroupCodeSuggestions(
  query: string,
  enabled: boolean,
): { codes: string[]; failed: boolean } {
  const { fetchGroups } = useOfficerApi();
  return useCodeSuggestions(query.trim(), enabled, fetchGroups);
}
