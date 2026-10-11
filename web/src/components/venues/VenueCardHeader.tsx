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
import { useTranslation } from "react-i18next";

import { CardHeader, CardTitle } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { ProviderIcon } from "./ProviderIcon";

const ERROR_TONE_CLASS = {
  danger: "text-[var(--danger)]",
  warn: "text-[var(--warn)]",
} as const;

/** Presentation-only header content assembled by the owning page. */
export interface VenueCardHeaderProps {
  providerType: string;
  title: string;
  /** Title element ID for the owning card's aria-labelledby. */
  titleId?: string;
  /** Omit to hide the description; an empty string keeps its paragraph. */
  description?: string;
  /** Undefined, null, booleans, and empty strings hide the badges row. */
  badges?: ReactNode;
  /** Already formatted error text; only non-empty strings show the paragraph. */
  error?: string;
  /** `warn` for a source that still works but reports a problem; `danger`
   *  by default. */
  errorTone?: "danger" | "warn";
  links?: { siteUrl?: string; docsUrl?: string; symbolsUrl?: string };
  actions?: ReactNode;
}

/** Provider card heading, status content, reference links, and actions. */
export function VenueCardHeader({
  providerType,
  title,
  titleId,
  description,
  badges,
  error,
  errorTone = "danger",
  links,
  actions,
}: VenueCardHeaderProps) {
  const { t } = useTranslation("venues");
  const siteUrl = links?.siteUrl;
  const docsUrl = links?.docsUrl;
  const symbolsUrl = links?.symbolsUrl;
  return (
    <CardHeader className="flex-row items-center justify-between gap-3">
      <div className="flex min-w-0 items-start gap-3">
        <ProviderIcon
          type={providerType}
          className="mt-0.5 h-5 w-5 shrink-0 text-accent"
        />
        <div className="min-w-0">
          <CardTitle id={titleId} className="truncate">
            {title}
          </CardTitle>
          {description !== undefined && (
            <p className="mt-1 max-w-3xl text-xs text-muted">{description}</p>
          )}
          {badges !== undefined &&
            badges !== null &&
            typeof badges !== "boolean" &&
            badges !== "" && (
              <div className="mt-1 flex flex-wrap items-center gap-2">
                {badges}
              </div>
            )}
          {error !== undefined && error !== "" && (
            <p
              className={cn(
                "mt-1.5 break-words text-xs",
                ERROR_TONE_CLASS[errorTone],
              )}
            >
              {error}
            </p>
          )}
          {(siteUrl || docsUrl || symbolsUrl) && (
            <div className="mt-1.5 flex flex-wrap gap-3">
              {siteUrl && (
                <a
                  href={siteUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                >
                  <ExternalLink className="h-3 w-3" />
                  {t("references.site")}
                </a>
              )}
              {docsUrl && (
                <a
                  href={docsUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                >
                  <ExternalLink className="h-3 w-3" />
                  {t("references.docs")}
                </a>
              )}
              {symbolsUrl && (
                <a
                  href={symbolsUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center gap-1 text-xs text-muted hover:text-text"
                >
                  <ExternalLink className="h-3 w-3" />
                  {t("references.symbols")}
                </a>
              )}
            </div>
          )}
        </div>
      </div>
      <div className="flex shrink-0 gap-2">{actions}</div>
    </CardHeader>
  );
}
