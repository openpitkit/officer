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

/** Render a barrier's kind/value map as labeled chips (e.g. max_orders=100).
 *  Kinds are shown in a stable order so rows stay scannable. */
export function ValueChips({ values }: { values: Record<string, string> }) {
  const entries = Object.entries(values).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) {
    return <span className="text-xs text-muted">—</span>;
  }
  return (
    <div className="flex flex-wrap gap-1.5">
      {entries.map(([kind, value]) => (
        <span
          key={kind}
          className="inline-flex items-center gap-1 rounded-badge border border-[var(--tag-border)] px-1.5 py-0.5 text-[0.6875rem]"
        >
          <span className="text-muted">{kind}</span>
          <span className="text-muted">=</span>
          <span className="nums text-text">{value}</span>
        </span>
      ))}
    </div>
  );
}
