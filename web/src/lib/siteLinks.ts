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

const SITES = {
  officer: "https://officer.openpit.dev",
  openpit: "https://openpit.dev",
  wiki: "https://wiki.openpit.dev",
} as const;

/**
 * Builds a site URL whose officer marker identifies visits from the panel.
 * Paths start with "/"; fragments are nonempty; neither contains "#".
 */
export function siteLink(
  site: keyof typeof SITES,
  path?: string,
  fragment?: string,
): string {
  if (path !== undefined && (!path.startsWith("/") || path.includes("#"))) {
    throw new TypeError('Site link path must start with "/" and contain no "#"');
  }
  if (fragment !== undefined && (fragment === "" || fragment.includes("#"))) {
    throw new TypeError('Site link fragment must be nonempty and contain no "#"');
  }
  const suffix = path === undefined ? "" : path;
  const marker = suffix.includes("?") ? "&officer" : "?officer";
  const hash = fragment === undefined ? "" : `#${fragment}`;
  return `${SITES[site]}${suffix}${marker}${hash}`;
}
