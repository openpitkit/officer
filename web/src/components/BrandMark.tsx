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

/** The Pit bar-chart glyph, in the current accent color. Mirrors the website
 *  favicon (pit/docs/favicon-*.svg) but recolored to follow the theme
 *  accent. */
export function BrandMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="-4 -4 72 72"
      role="img"
      aria-label="Pit logo"
      className={className}
      fill="none"
    >
      <rect x="4" y="38" width="36" height="10" rx="2" className="fill-muted" />
      <rect
        x="14"
        y="26"
        width="36"
        height="10"
        rx="2"
        className="fill-muted-lt"
      />
      <rect
        x="24"
        y="14"
        width="36"
        height="10"
        rx="2"
        className="fill-accent"
      />
    </svg>
  );
}
