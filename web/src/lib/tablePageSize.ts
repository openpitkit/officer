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

import { useState } from "react";

import {
  readStoredPreference,
  writeStoredPreference,
} from "@/lib/browserStorage";

const TABLE_PAGE_SIZES = [5, 50, 100, 500] as const;

export function useTablePageSizes(): readonly number[] {
  return TABLE_PAGE_SIZES;
}

export function usePersistentPageSize(
  storageKey: string,
  fallback = 50,
): [number, (value: number) => void] {
  const pageSizes = useTablePageSizes();
  const [value, setValue] = useState(() => {
    const stored = Number(readStoredPreference(storageKey));
    if (pageSizes.includes(stored)) {
      return stored;
    }
    return pageSizes.includes(fallback) ? fallback : pageSizes[0];
  });
  return [
    value,
    (next: number) => {
      setValue(next);
      writeStoredPreference(storageKey, String(next));
    },
  ];
}
