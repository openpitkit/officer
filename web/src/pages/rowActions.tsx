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
// Please see https://openpit.dev and the OWNERS file for details.

import type { TFunction } from "i18next";
import { useNavigate } from "react-router-dom";

import type { Account, Group, Limit } from "@/api/types";
import {
  DeleteButton as RowDeleteButton,
  EditButton,
  HistoryButton,
  PoliciesButton,
  PositionsButton,
  TradingButton,
} from "@/framework";
import { absoluteAppUrl } from "@/lib/shareLink";

export interface AccountRowActionContext {
  t: TFunction<"accounts">;
  onDelete: (account: Account) => void;
}

export interface GroupRowActionContext {
  t: TFunction<"accounts">;
  onDelete: (group: Group) => void;
}

export interface LimitRowActionContext {
  t: TFunction<"policies">;
  onEdit: (limit: Limit) => void;
  onDelete: (limit: Limit) => void;
}

export function AccountPositionsAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  const navigate = useNavigate();
  const path = `/positions?account=${encodeURIComponent(account.code)}`;

  return (
    <PositionsButton
      href={absoluteAppUrl(path)}
      title={ctx.t("accounts.links.positions")}
      onClick={() => navigate(path)}
    />
  );
}

export function AccountTradingAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  const navigate = useNavigate();
  const path = `/trading?account=${encodeURIComponent(account.code)}`;

  return (
    <TradingButton
      href={absoluteAppUrl(path)}
      title={ctx.t("accounts.links.trading")}
      onClick={() => navigate(path)}
    />
  );
}

export function AccountPoliciesAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  const navigate = useNavigate();
  const path = `/policies?account=${encodeURIComponent(account.code)}`;

  return (
    <PoliciesButton
      href={absoluteAppUrl(path)}
      title={ctx.t("accounts.links.policies")}
      onClick={() => navigate(path)}
    />
  );
}

export function AccountAuditAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  const navigate = useNavigate();
  const path = `/audit?account=${encodeURIComponent(account.code)}`;

  return (
    <HistoryButton
      href={absoluteAppUrl(path)}
      title={ctx.t("accounts.links.audit")}
      onClick={() => navigate(path)}
    />
  );
}

export function AccountDeleteAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return (
    <RowDeleteButton
      title={ctx.t("accounts.actions.deleteTitle")}
      onClick={() => ctx.onDelete(account)}
    />
  );
}

export function GroupDeleteAction({
  group,
  ctx,
}: {
  group: Group;
  ctx: GroupRowActionContext;
}) {
  return (
    <RowDeleteButton
      title={ctx.t("groups.actions.deleteTitle")}
      onClick={() => ctx.onDelete(group)}
    />
  );
}

export function LimitEditAction({
  limit,
  ctx,
}: {
  limit: Limit;
  ctx: LimitRowActionContext;
}) {
  return (
    <EditButton
      title={ctx.t("table.editAriaLabel")}
      onClick={() => ctx.onEdit(limit)}
    />
  );
}

export function LimitDeleteAction({
  limit,
  ctx,
}: {
  limit: Limit;
  ctx: LimitRowActionContext;
}) {
  return (
    <RowDeleteButton
      title={ctx.t("table.deleteAriaLabel")}
      onClick={() => ctx.onDelete(limit)}
    />
  );
}
