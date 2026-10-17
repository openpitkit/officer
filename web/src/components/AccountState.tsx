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

import { StatusDot } from "@/components/StatusDot";
import { Badge } from "@/components/ui/badge";

/** Whether the account can trade now: `blocked` is its effective block, the
 *  account's own or its group's. */
export function AccountStatusBadge({ blocked }: { blocked: boolean }) {
  const { t } = useTranslation("accounts");
  return blocked ? (
    <Badge variant="danger" className="shrink-0">
      <StatusDot tone="danger" />
      {t("accounts.status.blocked")}
    </Badge>
  ) : (
    <Badge variant="ok" className="shrink-0">
      <StatusDot tone="ok" />
      {t("accounts.status.active")}
    </Badge>
  );
}
