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
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { AlertTriangle, Users } from "lucide-react";

import type { Account as AccountRecord } from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import {
  AccountCurrencyDialog,
  BlockAccountDialog,
  EffectiveCurrency,
  UnblockAccountConfirm,
} from "@/components/AccountDialogs";
import { AccountStatusBadge } from "@/components/AccountState";
import { Notice } from "@/components/Notice";
import { Page } from "@/components/Page";
import { ErrorState, StaleState, TableSkeleton } from "@/components/PageStates";
import { StatRow } from "@/components/StatRow";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { accountScope } from "@/lib/accountScope";

type AccountDialog = "currency" | "block" | "unblock";

/** The account's currency and block state, and the shared dialogs that
 *  change them. */
function AccountSettings({
  account,
  onChanged,
}: {
  account: AccountRecord;
  onChanged: () => void;
}) {
  const { t } = useTranslation("accounts");
  const [dialog, setDialog] = useState<AccountDialog | null>(null);

  const onOpenChange = (open: boolean) => {
    if (!open) setDialog(null);
  };
  const onDone = () => {
    setDialog(null);
    onChanged();
  };

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>{t("account.settingsTitle")}</CardTitle>
        </CardHeader>
        <CardContent>
          <StatRow
            label={t("accounts.columns.currency")}
            mono={false}
            value={
              <span className="flex flex-wrap items-center justify-end gap-2">
                <span className="nums">
                  <EffectiveCurrency account={account} />
                </span>
                <Button
                  variant="outline"
                  size="sm"
                  onClick={() => setDialog("currency")}
                >
                  {t("accounts.actions.editCurrencyTitle")}
                </Button>
              </span>
            }
          />
          <StatRow
            label={t("accounts.columns.status")}
            mono={false}
            value={
              <span className="flex flex-wrap items-center justify-end gap-2">
                <AccountStatusBadge blocked={account.blocked} />
                {/* As on the Accounts page, the action follows the account's
                    own block: a group's block is not the account's to lift. */}
                {account.accountBlocked ? (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setDialog("unblock")}
                  >
                    {t("accounts.actions.unblock")}
                  </Button>
                ) : (
                  <Button
                    variant="outline"
                    size="sm"
                    onClick={() => setDialog("block")}
                  >
                    {t("accounts.actions.block")}
                  </Button>
                )}
              </span>
            }
          />
          {account.blocked && (
            <div className="space-y-1 py-2">
              <p className="text-xs text-muted">{t("blockDetails.reason")}</p>
              <p className="whitespace-pre-wrap break-words text-sm text-text">
                {account.blockReason || t("blockDetails.noReason")}
              </p>
              {!account.accountBlocked && (
                <p className="text-xs text-muted-lt">
                  {t("account.notOwnBlock")}
                </p>
              )}
            </div>
          )}
        </CardContent>
      </Card>
      <BlockAccountDialog
        account={account}
        open={dialog === "block"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <UnblockAccountConfirm
        account={account}
        open={dialog === "unblock"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      {dialog === "currency" && (
        <AccountCurrencyDialog
          account={account}
          open
          onOpenChange={onOpenChange}
          onDone={onDone}
        />
      )}
    </>
  );
}

/** The page of the one account Officer acts on while accounts are hidden. */
export function Account() {
  const { t } = useTranslation("accounts");
  const code = accountScope().account;
  // A list row, not GET /accounts/{code}: only list rows carry the position
  // count the currency dialog warns about.
  const { load, reload } = useAccounts({ code, codeMatch: "exact" });
  const account =
    load.state === "ready"
      ? load.data.find((row) => row.code === code)
      : undefined;

  return (
    <Page
      title={t("account.title")}
      actions={
        <Button asChild variant="ghost" size="sm">
          <Link to="/service#account-mode">
            <Users className="h-3.5 w-3.5" />
            {t("account.switchToSeveral")}
          </Link>
        </Button>
      }
    >
      {load.state === "loading" && <TableSkeleton rows={2} cols={2} />}
      <StaleState load={load} reload={reload} />
      {load.state === "error" && (
        <ErrorState message={load.error} onRetry={reload} />
      )}
      {load.state === "ready" &&
        (account === undefined ? (
          <Notice
            tone="warn"
            icon={AlertTriangle}
            title={t("account.missing.title")}
          >
            <p>{t("account.missing.detail")}</p>
          </Notice>
        ) : (
          <AccountSettings account={account} onChanged={reload} />
        ))}
    </Page>
  );
}
