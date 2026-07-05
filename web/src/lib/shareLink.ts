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

/** Resolve an in-app path to an absolute, shareable URL against the current
 *  origin. Falls back to the raw path during SSR / non-browser rendering. */
export function absoluteAppUrl(path: string): string {
  if (typeof window === "undefined") {
    return path;
  }
  return new URL(path, window.location.origin).toString();
}

/** Join a base path with a `URLSearchParams`, appending `?query` only when it
 *  carries at least one param so a filter-free link stays clean. */
export function shareUrl(path: string, params: URLSearchParams): string {
  const query = params.toString();
  return absoluteAppUrl(query === "" ? path : `${path}?${query}`);
}
