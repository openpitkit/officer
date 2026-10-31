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

import { useTranslation } from "react-i18next";

import { ExternalLinkCard } from "@/components/ExternalLinkCard";
import { cn } from "@/lib/utils";

const PRODUCT_LINKS = [
  { key: "website", href: "https://officer.openpit.dev?officer" },
  {
    key: "issues",
    href: "https://github.com/openpitkit/officer/issues?officer",
  },
  {
    key: "discussions",
    href: "https://github.com/openpitkit/officer/discussions?officer",
  },
  { key: "openpit", href: "https://openpit.dev?officer" },
] as const;

/**
 * Officer's website, issue tracker and discussions, and the OpenPit engine,
 * as a grid of link cards.
 */
export function ProductLinks({ className }: { className?: string }) {
  const { t } = useTranslation();
  return (
    <div className={cn("grid gap-2 sm:grid-cols-2", className)}>
      {PRODUCT_LINKS.map((link) => (
        <ExternalLinkCard
          key={link.href}
          href={link.href}
          label={t(`about.links.${link.key}`)}
        />
      ))}
    </div>
  );
}
