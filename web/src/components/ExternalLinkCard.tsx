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

import { ExternalLink } from "lucide-react";
import type { ReactNode } from "react";

/** A link to an outside page as a small card; it opens in a new tab. */
export function ExternalLinkCard({
  href,
  label,
  icon,
}: {
  href: string;
  label: string;
  icon?: ReactNode;
}) {
  return (
    <a
      href={href}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center justify-between gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt transition-colors hover:border-border-hover hover:bg-card-hover-bg hover:text-accent"
    >
      <span>
        {icon}
        {label}
      </span>
      <ExternalLink className="h-3.5 w-3.5 shrink-0" />
    </a>
  );
}
