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
import { useAccountMode } from "@/api/accountMode";
import { AccountModePanel } from "@/components/AccountModeChoice";
import { AccountPnlResetSchedule } from "@/components/AccountPnlReset";
import { useOfficerApi } from "@/framework";
import { accountScope } from "@/lib/accountScope";

/** Account mode and the host's optional daily P&L reset settings. */
export function AccountManagementSettings() {
  const { t } = useTranslation("service");
  const poll = useAccountMode();
  const { restartService } = useOfficerApi();
  return (
    <div className="flex flex-col gap-y-6">
      <section className="flex flex-col gap-y-4">
        <h2 className="text-sm font-medium">{t("accountMode.title")}</h2>
        <AccountModePanel
          poll={poll}
          restart={restartService}
          disabled={false}
          onSaved={poll.reload}
        />
      </section>
      {accountScope().accountPnlReset && <AccountPnlResetSchedule />}
    </div>
  );
}
