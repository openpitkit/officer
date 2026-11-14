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
import { AlertTriangle } from "lucide-react";

import type { Account as AccountRecord } from "@/api/types";
import { useAccounts } from "@/api/useAccounts";
import { useTrading } from "@/api/useTrading";
import { useNavigate } from "react-router-dom";
import {
  AssignGroupDialog,
  BlockedDetailsDialog,
  DeleteAccountConfirm,
  EditAccountMetadataDialog,
  EditAccountNotesDialog,
} from "./Accounts";
import { VenueAccessDialogContainer } from "./VenueAccessDialogContainer";
import {
  AccountCurrencyDialog,
  BlockAccountDialog,
  EffectiveCurrency,
  UnblockAccountConfirm,
} from "@/components/AccountDialogs";
import { AccountStatusBadge } from "@/components/AccountState";
import {
  AccountRealizedPnl,
  ResetAccountPnlConfirm,
} from "@/components/AccountPnlReset";
import { AccountVenueAccess } from "@/components/AccountVenueAccess";
import {
  ActionButton,
  DeleteButton,
  HistoryButton,
  IdCell,
  PoliciesButton,
  PositionsButton,
  RowActions,
  TradingButton,
} from "@/components/data/RowActions";
import { Notice } from "@/components/Notice";
import { Page } from "@/components/Page";
import { ErrorState, StaleState, TableSkeleton } from "@/components/PageStates";
import { StatRow } from "@/components/StatRow";
import { Button } from "@/components/ui/button";
import { accountScope } from "@/lib/accountScope";
import { absoluteAppUrl } from "@/lib/shareLink";
import { AccountManagementSettings } from "@/components/AccountManagementSettings";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";

type AccountDialog =
  | "currency"
  | "block"
  | "unblock"
  | "pnl-reset"
  | "identity"
  | "group"
  | "notes"
  | "delete"
  | "details"
  | "venues";

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
  const { t: tc } = useTranslation("common");
  const [dialog, setDialog] = useState<AccountDialog | null>(null);
  const trading = useTrading();
  const navigate = useNavigate();
  const managementUnavailable = !accountScope().showsAccounts;
  const managementHint = managementUnavailable
    ? t("accounts.actions.managementUnavailable")
    : undefined;
  const onOpenChange = (open: boolean) => {
    if (!open) setDialog(null);
  };
  const onDone = () => {
    setDialog(null);
    onChanged();
    trading.reload();
  };
  const accountPath = (path: string) =>
    `/${path}?account=${encodeURIComponent(account.code)}`;
  const go = (path: string) => navigate(accountPath(path));
  return (
    <>
      <section className="flex w-fit max-w-full flex-col gap-y-4">
        <h2 className="text-sm font-medium">{t("account.settingsTitle")}</h2>
        <div>
          <StaleState load={trading.load} reload={trading.reload} />
          {trading.load.state === "error" && (
            <ErrorState message={trading.load.error} onRetry={trading.reload} />
          )}
          <div className="grid grid-cols-[max-content_minmax(0,1fr)] items-center gap-x-4 gap-y-4 [&>div]:contents [&>div>span:last-child]:text-left [&>div>span:last-child>div]:inline-flex">
            <StatRow
              label={t("accounts.columns.account")}
              mono={false}
              value={
                <span className="flex flex-wrap items-center justify-start gap-2">
                  {account.title && account.title !== account.code && (
                    <span className="nums">{account.title}</span>
                  )}
                  <IdCell
                    value={account.code}
                    copyTitle={tc("rowActions.copyIdTitle", {
                      entity: account.code,
                    })}
                    copiedTitle={tc("rowActions.copiedId")}
                  />
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={managementUnavailable}
                    title={managementHint}
                    onClick={() => setDialog("identity")}
                  >
                    {t("accounts.actions.editTitle")}
                  </Button>
                </span>
              }
            />
            <StatRow
              label={t("accounts.columns.group")}
              mono={false}
              value={
                <span className="flex flex-wrap items-center justify-start gap-2">
                  <span className="nums">
                    {account.group || t("accounts.groupCell.noGroup")}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    disabled={managementUnavailable}
                    title={managementHint}
                    onClick={() => setDialog("group")}
                  >
                    {t("accounts.groupCell.editTitle")}
                  </Button>
                </span>
              }
            />
            <StatRow
              label={t("accounts.columns.pnl")}
              mono={false}
              value={
                <AccountRealizedPnl
                  account={account}
                  onReset={
                    accountScope().accountPnlReset
                      ? () => setDialog("pnl-reset")
                      : undefined
                  }
                />
              }
            />
            <StatRow
              label={t("accounts.columns.currency")}
              mono={false}
              value={
                <span className="flex flex-wrap items-center justify-start gap-2">
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
              label={t("accounts.columns.positions")}
              mono={false}
              value={
                <span className="flex items-center justify-start gap-2">
                  <span className="nums">{account.positionCount ?? 0}</span>
                  <PositionsButton
                    title={t("accounts.links.positions")}
                    href={absoluteAppUrl(accountPath("positions"))}
                    onClick={() => go("positions")}
                  />
                </span>
              }
            />
            <StatRow
              label={t("accounts.columns.venueAccess")}
              mono={false}
              value={
                <AccountVenueAccess
                  account={account.code}
                  group={account.group}
                  snapshot={
                    trading.load.state === "ready" ? trading.load.data : null
                  }
                  onOpen={() => setDialog("venues")}
                />
              }
            />
            <StatRow
              label={t("accounts.columns.status")}
              mono={false}
              value={
                <span className="flex flex-wrap items-center justify-start gap-2">
                  <AccountStatusBadge blocked={account.blocked} />
                  {account.blocked && (
                    <ActionButton
                      icon="view"
                      onClick={() => setDialog("details")}
                      title={t("accounts.actions.viewBlockDetails")}
                    />
                  )}
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
            <StatRow
              label={t("accounts.columns.notes")}
              mono={false}
              className="items-start"
              value={
                <span className="flex items-start justify-start gap-2">
                  <span className="min-w-0 whitespace-pre-wrap break-words">
                    {account.notes || "-"}
                  </span>
                  <Button
                    variant="outline"
                    size="sm"
                    className="shrink-0"
                    onClick={() => setDialog("notes")}
                  >
                    {t("accounts.actions.editNotesTitle")}
                  </Button>
                </span>
              }
            />
            <StatRow
              label={t("accounts.columns.actions")}
              mono={false}
              value={
                <RowActions>
                  <TradingButton
                    title={t("accounts.links.trading")}
                    href={absoluteAppUrl(accountPath("trading"))}
                    onClick={() => go("trading")}
                  />
                  <PoliciesButton
                    title={t("accounts.links.policies")}
                    href={absoluteAppUrl(accountPath("policies"))}
                    onClick={() => go("policies")}
                  />
                  <HistoryButton
                    title={t("accounts.links.audit")}
                    href={absoluteAppUrl(accountPath("audit"))}
                    onClick={() => go("audit")}
                  />
                  <DeleteButton
                    disabled={managementUnavailable}
                    title={
                      managementHint
                        ? `${tc("rowActions.deleteTitle", { entity: account.code })}: ${managementHint}`
                        : tc("rowActions.deleteTitle", { entity: account.code })
                    }
                    onClick={() => setDialog("delete")}
                  />
                </RowActions>
              }
            />
          </div>
          {account.blocked && (
            <div className="flex flex-col gap-y-1 py-2">
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
        </div>
      </section>
      <EditAccountMetadataDialog
        account={account}
        open={dialog === "identity"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <AssignGroupDialog
        account={account}
        groupSuggestions={account.group ? [account.group] : []}
        open={dialog === "group"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <EditAccountNotesDialog
        account={account}
        open={dialog === "notes"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <DeleteAccountConfirm
        account={account}
        trading={trading}
        open={dialog === "delete"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <BlockedDetailsDialog
        target={dialog === "details" ? { kind: "account", account } : null}
        open={dialog === "details"}
        onOpenChange={onOpenChange}
        onUnblock={() => setDialog("unblock")}
      />
      <BlockAccountDialog
        account={account}
        open={dialog === "block"}
        onOpenChange={onOpenChange}
        onDone={onDone}
      />
      <ResetAccountPnlConfirm
        account={dialog === "pnl-reset" ? account : null}
        onClose={() => setDialog(null)}
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
      {dialog === "venues" && (
        <VenueAccessDialogContainer
          subject={{ account: account.code }}
          trading={trading}
          onClose={onDone}
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
    <Page title={t("account.title")}>
      <Tabs defaultValue="account">
        <TabsList>
          <TabsTrigger value="account">{t("account.title")}</TabsTrigger>
          <TabsTrigger value="settings">{t("tabs.settings")}</TabsTrigger>
        </TabsList>
        <TabsContent value="settings" className="mt-6">
          <AccountManagementSettings />
        </TabsContent>
        <TabsContent value="account" className="mt-6">
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
        </TabsContent>
      </Tabs>
    </Page>
  );
}
