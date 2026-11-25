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

import { useState } from "react";
import { ArrowLeft, ChartNoAxesCombined, Gauge, UserCheck } from "lucide-react";
import { useTranslation } from "react-i18next";

import { SelectionTile } from "@/components/SelectionTile";
import { Button } from "@/components/ui/button";
import { PolicyWikiLink } from "@/components/PolicyWikiLink";
import { PolicyVideoButton } from "@/components/PolicyVideoButton";
import {
  getPolicies,
  getPolicyCatalogEntry,
  policyCatalogDescription,
  policyCatalogLabel,
  type Policy,
} from "@/api/vocabulary";
import type { PolicyGroup } from "@/framework/registries/vocabulary";

const GROUPS = [
  { id: "orders", icon: Gauge },
  { id: "pnl", icon: ChartNoAxesCombined },
  { id: "approval", icon: UserCheck },
] satisfies { id: PolicyGroup; icon: typeof Gauge }[];

/** Selects a policy by semantic group without an intermediate confirmation. */
export function PolicyPicker({
  onSelect,
}: {
  onSelect: (policy: Policy) => void;
}) {
  const { t } = useTranslation("policies");
  const [group, setGroup] = useState<PolicyGroup | null>(null);

  if (group === null) {
    return (
      <div className="grid gap-3 sm:grid-cols-3">
        {GROUPS.map(({ id, icon: Icon }) => (
          <SelectionTile
            key={id}
            title={t(`picker.groups.${id}.title`)}
            description={t(`picker.groups.${id}.description`)}
            icon={<Icon className="mt-0.5 h-5 w-5 shrink-0 text-muted" />}
            onSelect={() => setGroup(id)}
          />
        ))}
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <nav
        className="flex items-center gap-3"
        aria-label={t("picker.groupsLabel")}
      >
        <Button variant="ghost" size="sm" onClick={() => setGroup(null)}>
          <ArrowLeft className="mr-1.5 h-3.5 w-3.5" />
          {t("picker.allGroups")}
        </Button>
        <span className="text-sm text-muted-lt">
          {t(`picker.groups.${group}.title`)}
        </span>
      </nav>
      <div className="grid min-h-32 gap-3 sm:grid-cols-2">
        {getPolicies().map((policy) => {
          const entry = getPolicyCatalogEntry(policy);
          if (entry?.group !== group) {
            return null;
          }
          return (
            <SelectionTile
              key={policy}
              title={policyCatalogLabel(t, policy)}
              description={policyCatalogDescription(t, policy)}
              onSelect={() => onSelect(policy)}
            >
              <div className="flex items-center justify-between gap-2 border-t border-border px-4 py-2">
                <PolicyWikiLink entry={entry} />
                <PolicyVideoButton policy={policy} size="link" />
              </div>
            </SelectionTile>
          );
        })}
      </div>
    </div>
  );
}
