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

import type { PolicyCatalogEntry } from "@/framework/registries/vocabulary";

/** Opens the selected policy's own wiki section without selecting the tile. */
export function PolicyWikiLink({ entry }: { entry: PolicyCatalogEntry }) {
  const { t } = useTranslation("policies");
  return (
    <a
      href={entry.wikiUrl}
      target="_blank"
      rel="noopener noreferrer"
      className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
    >
      {t("wikiDetails")}
      <ExternalLink className="h-3 w-3" />
    </a>
  );
}
