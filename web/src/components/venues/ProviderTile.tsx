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
import { useTranslation } from "react-i18next";

import { Card } from "@/components/ui/card";
import { ProviderIcon } from "./ProviderIcon";

/** Caller-resolved provider identity, display text, and reference URLs. */
export interface VenueProviderOption {
  type: string;
  title: string;
  /** Omit to hide the description; an empty string keeps its paragraph. */
  description?: string;
  siteUrl?: string;
  docsUrl?: string;
  symbolsUrl?: string;
}

/** Provider content and selection callback for a picker tile. */
export interface ProviderTileProps {
  provider: VenueProviderOption;
  onSelect: (provider: VenueProviderOption) => void;
}

/** Renders a selectable provider with its optional reference links. */
export function ProviderTile({ provider, onSelect }: ProviderTileProps) {
  const { t } = useTranslation("venues");
  const { siteUrl, docsUrl, symbolsUrl } = provider;

  return (
    <Card className="overflow-hidden">
      <button
        type="button"
        className="block w-full p-4 text-left transition-colors hover:bg-surface-hover"
        onClick={() => onSelect(provider)}
      >
        <div className="flex items-start gap-3">
          <ProviderIcon
            type={provider.type}
            className="mt-0.5 h-5 w-5 shrink-0 text-accent"
          />
          <div className="min-w-0">
            <p className="font-semibold text-text">{provider.title}</p>
            {provider.description !== undefined && (
              <p className="mt-1 text-xs text-muted">{provider.description}</p>
            )}
          </div>
        </div>
      </button>
      {(siteUrl || docsUrl || symbolsUrl) && (
        <div className="flex flex-wrap gap-3 border-t border-border px-4 py-2">
          {siteUrl && (
            <a
              href={siteUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.site")}
            </a>
          )}
          {docsUrl && (
            <a
              href={docsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.docs")}
            </a>
          )}
          {symbolsUrl && (
            <a
              href={symbolsUrl}
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
            >
              <ExternalLink className="h-3 w-3" />
              {t("providers.symbols")}
            </a>
          )}
        </div>
      )}
    </Card>
  );
}
