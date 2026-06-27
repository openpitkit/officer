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
  Ban,
  CircleCheck,
  Coins,
  History,
  Pencil,
  ShieldCheck,
  Trash2,
} from "lucide-react";
import { Link } from "react-router-dom";

import type { Account, Group, Limit } from "@/api/types";
import { Button } from "@/components/ui/button";

export interface AccountRowActionContext {
  t: TFunction<"accounts">;
  onEditNotes: (account: Account) => void;
  onBlock: (account: Account) => void;
  onUnblock: (account: Account) => void;
  onDelete: (account: Account) => void;
}

export interface GroupRowActionContext {
  t: TFunction<"accounts">;
  onEditNotes: (group: Group) => void;
  onBlock: (group: Group) => void;
  onUnblock: (group: Group) => void;
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

export function AccountNotesAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => ctx.onEditNotes(account)}
      title={ctx.t("accounts.actions.editNotesTitle")}
    >
      {ctx.t("accounts.actions.notes")}
    </Button>
  );
}

export function AccountBlockAction({
  account,
  ctx,
}: {
  account: Account;
  ctx: AccountRowActionContext;
}) {
  return account.blocked ? (
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onUnblock(account)}
    >
      <CircleCheck className="h-3.5 w-3.5" />
      {ctx.t("accounts.actions.unblock")}
    </Button>
  ) : (
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onBlock(account)}
    >
      <Ban className="h-3.5 w-3.5" />
      {ctx.t("accounts.actions.block")}
    </Button>
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
    <Button
      variant="ghost"
      size="sm"
      onClick={() => ctx.onDelete(account)}
      title={ctx.t("accounts.actions.deleteTitle")}
    >
      <Trash2 className="h-3.5 w-3.5" />
    </Button>
  );
}

export function GroupNotesAction({
  group,
  ctx,
}: {
  group: Group;
  ctx: GroupRowActionContext;
}) {
  return (
    <Button
      variant="ghost"
      size="sm"
      onClick={() => ctx.onEditNotes(group)}
      title={ctx.t("groups.actions.editNotesTitle")}
    >
      {ctx.t("groups.actions.notes")}
    </Button>
  );
}

export function GroupBlockAction({
  group,
  ctx,
}: {
  group: Group;
  ctx: GroupRowActionContext;
}) {
  return group.blocked ? (
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onUnblock(group)}
    >
      <CircleCheck className="h-3.5 w-3.5" />
      {ctx.t("groups.actions.unblock")}
    </Button>
  ) : (
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onBlock(group)}
    >
      <Ban className="h-3.5 w-3.5" />
      {ctx.t("groups.actions.block")}
    </Button>
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
    <Button
      variant="ghost"
      size="sm"
      onClick={() => ctx.onDelete(group)}
      title={ctx.t("groups.actions.deleteTitle")}
      className="text-[var(--danger)] hover:text-[var(--danger)]"
    >
      <Trash2 className="h-3.5 w-3.5" />
      {ctx.t("groups.actions.delete")}
    </Button>
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
    <Button
      variant="outline"
      size="sm"
      onClick={() => ctx.onEdit(limit)}
      aria-label={ctx.t("table.editAriaLabel")}
    >
      <Pencil className="h-3.5 w-3.5" />
      {ctx.t("table.edit")}
    </Button>
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

