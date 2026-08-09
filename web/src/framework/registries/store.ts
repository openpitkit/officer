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

export interface OrderedEntry {
  id: string;
  order: number;
}

interface StoredEntry<Entry> {
  entry: Entry;
  sequence: number;
}

export function createOrderedRegistry<Entry extends OrderedEntry>() {
  const entries = new Map<string, StoredEntry<Entry>>();
  let nextSequence = 0;

  return {
    register(entry: Entry): void {
      const existing = entries.get(entry.id);
      entries.set(entry.id, {
        entry,
        sequence: existing?.sequence ?? nextSequence++,
      });
    },
    unregister(id: string): void {
      entries.delete(id);
    },
    get(): readonly Entry[] {
      return Array.from(entries.values())
        .sort((left, right) => {
          const byOrder = left.entry.order - right.entry.order;
          return byOrder !== 0 ? byOrder : left.sequence - right.sequence;
        })
        .map(({ entry }) => entry);
    },
    reset(): void {
      entries.clear();
      nextSequence = 0;
    },
  };
}
