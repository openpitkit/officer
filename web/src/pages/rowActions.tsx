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
import {
  ArrowLeftRight,
  Coins,
  History,
  Pencil,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { Link } from "react-router-dom";

import type { Account, Group, Limit } from "@/api/types";
import { RowActionButton } from "@/components/RowActionButton";
import { Button } from "@/components/ui/button";

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
  return (
    <Link
      to={`/positions?account=${encodeURIComponent(account.code)}`}
      title={ctx.t("accounts.links.positions")}
      aria-label={ctx.t("accounts.links.positions")}
      className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
    >
      <Coins className="h-3.5 w-3.5 text-muted" />
    </Link>
  );
}

export function AccountTradingAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return (
    <Link
      to={`/trading?account=${encodeURIComponent(account.code)}`}
      title={ctx.t("accounts.links.trading")}
      aria-label={ctx.t("accounts.links.trading")}
      className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
    >
      <ArrowLeftRight className="h-3.5 w-3.5 text-muted" />
    </Link>
  );
}

export function AccountPoliciesAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return (
    <Link
      to={`/policies?account=${encodeURIComponent(account.code)}`}
      title={ctx.t("accounts.links.policies")}
      aria-label={ctx.t("accounts.links.policies")}
      className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
    >
      <ShieldCheck className="h-3.5 w-3.5 text-muted" />
    </Link>
  );
}

export function AccountAuditAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return (
    <Link
      to={`/audit?account=${encodeURIComponent(account.code)}`}
      title={ctx.t("accounts.links.audit")}
      aria-label={ctx.t("accounts.links.audit")}
      className="inline-flex h-7 w-7 items-center justify-center rounded-badge transition-colors hover:bg-accent-dim"
    >
      <History className="h-3.5 w-3.5 text-muted" />
    </Link>
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
    <RowActionButton
      icon={Trash2}
      label={ctx.t("accounts.actions.deleteTitle")}
      variant="ghost"
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
    <RowActionButton
      icon={Trash2}
      label={ctx.t("groups.actions.deleteTitle")}
      variant="ghost"
      onClick={() => ctx.onDelete(group)}
      className="text-[var(--danger)] hover:text-[var(--danger)]"
    >
      {ctx.t("groups.actions.delete")}
    </RowActionButton>
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
    <RowActionButton
      icon={Pencil}
      label={ctx.t("table.editAriaLabel")}
      onClick={() => ctx.onEdit(limit)}
    >
      {ctx.t("table.edit")}
    </RowActionButton>
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
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onDelete(limit)}
      aria-label={ctx.t("table.deleteAriaLabel")}
    >
      <Trash2 className="h-3.5 w-3.5" />
    </Button>
  );
}
