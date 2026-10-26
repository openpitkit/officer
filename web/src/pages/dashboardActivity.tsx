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

// Dashboard-local helpers: activity grouping + audit action icons.

import {
  AlertTriangle,
  ArrowLeftRight,
  Ban,
  CircleDollarSign,
  ClipboardList,
  Coins,
  HelpCircle,
  Layers,
  Lock,
  LockOpen,
  Power,
  PlusCircle,
  RefreshCw,
  ShieldOff,
  Sliders,
  Trash2,
  UserCog,
  Users,
} from "lucide-react";
import type { ComponentType } from "react";

import type { ActivityEntry } from "@/api/types";
import { codeLabel } from "@/i18n/codeLabel";

// ---------------------------------------------------------------------------
// Activity grouping
// ---------------------------------------------------------------------------

export type ActivityGroup = {
  /** i18n key under the "dashboard" namespace for the column header. */
  labelKey: string;
  /** Route prefix for navigation: "trading" or "audit". */
  route: "trading" | "audit";
  entries: ActivityEntry[];
};

/** Maps an `ActivityEntry.kind` prefix to its group bucket. */
function kindBucket(
  kind: string,
): "trading" | "accounts" | "limits" | "groups" | "adjustments" | "other" {
  const k = kind.toLowerCase();
  if (
    k.startsWith("order") ||
    k.startsWith("trade") ||
    k.startsWith("fill") ||
    k.startsWith("exec")
  ) {
    return "trading";
  }
  if (k.startsWith("account")) {
    return "accounts";
  }
  if (k.startsWith("limit") || k.startsWith("policy")) {
    return "limits";
  }
  if (k.startsWith("group")) {
    return "groups";
  }
  if (
    k.startsWith("adjustment") ||
    k.startsWith("balance") ||
    k.startsWith("fund")
  ) {
    return "adjustments";
  }
  return "other";
}

const BUCKET_META: Record<
  ReturnType<typeof kindBucket>,
  { labelKey: string; route: "trading" | "audit" }
> = {
  trading: { labelKey: "activity.bucket.trading", route: "trading" },
  accounts: { labelKey: "activity.bucket.accounts", route: "audit" },
  limits: { labelKey: "activity.bucket.limits", route: "audit" },
  groups: { labelKey: "activity.bucket.groups", route: "audit" },
  adjustments: { labelKey: "activity.bucket.adjustments", route: "audit" },
  other: { labelKey: "activity.bucket.other", route: "audit" },
};

/** Bucket ordering - determines column order. */
const BUCKET_ORDER: ReturnType<typeof kindBucket>[] = [
  "trading",
  "accounts",
  "adjustments",
  "limits",
  "groups",
  "other",
];

/** Split activity entries into display groups, preserving relative order
 *  within each group and dropping empty buckets. */
export function groupActivity(entries: ActivityEntry[]): ActivityGroup[] {
  const map = new Map<ReturnType<typeof kindBucket>, ActivityEntry[]>();
  for (const e of entries) {
    const b = kindBucket(e.kind);
    const arr = map.get(b) ?? [];
    arr.push(e);
    map.set(b, arr);
  }
  return BUCKET_ORDER.filter((b) => map.has(b)).map((b) => ({
    labelKey: BUCKET_META[b].labelKey,
    route: BUCKET_META[b].route,
    entries: map.get(b)!,
  }));
}

// ---------------------------------------------------------------------------
// Audit action icon mapping
// ---------------------------------------------------------------------------

export type AuditIconMeta = {
  Icon: ComponentType<{ className?: string }>;
  variant: "neutral" | "ok" | "warn" | "danger" | "accent";
  /** i18n key under the "dashboard" namespace, or `null` for an action this
   *  mapping does not know. */
  titleKey: string | null;
};

/** Title keys of the audit actions that have no icon of their own. */
const AUDIT_ACTION_TITLE_KEYS = new Map<string, string>([
  ["hydrate", "audit.action.hydrate"],
  ["create_asset", "audit.action.createAsset"],
  ["update_asset", "audit.action.updateAsset"],
  ["delete_asset", "audit.action.deleteAsset"],
  ["set_notes", "audit.action.setAccountNotes"],
  ["set_group_notes", "audit.action.setGroupNotes"],
  ["set_group", "audit.action.setAccountGroup"],
  ["set_account_currency", "audit.action.setAccountCurrency"],
  ["set_group_currency", "audit.action.setGroupCurrency"],
  ["create_asset_class", "audit.action.createAssetClass"],
  ["update_asset_class", "audit.action.updateAssetClass"],
  ["delete_asset_class", "audit.action.deleteAssetClass"],
  ["execution_report", "audit.action.executionReport"],
  ["set_mcp_access", "audit.action.setMcpAccess"],
  ["set_setting", "audit.action.setSetting"],
  ["set_market_data", "audit.action.setMarketData"],
  ["set_trading", "audit.action.setTrading"],
  ["venue_fee_skipped", "audit.action.venueFeeSkipped"],
  ["export_backup", "audit.action.exportBackup"],
  ["restore_backup", "audit.action.restoreBackup"],
  ["export_business_csv", "audit.action.exportBusinessCsv"],
  ["generate_signing_key", "audit.action.generateSigningKey"],
  ["import_signing_key", "audit.action.importSigningKey"],
  ["set_signing_config", "audit.action.setSigningConfig"],
  ["approval_issued", "audit.action.approvalIssued"],
  ["approval_failed", "audit.action.approvalFailed"],
  ["approval_confirmed", "audit.action.approvalConfirmed"],
  ["approval_cancelled", "audit.action.approvalCancelled"],
  ["set_access_mode", "audit.action.setAccessMode"],
  ["issue_access_token", "audit.action.issueAccessToken"],
  ["set_panel_password", "audit.action.setPanelPassword"],
  ["issue_recovery_code", "audit.action.issueRecoveryCode"],
  ["use_recovery_code", "audit.action.useRecoveryCode"],
]);

/** The display label of an `AuditEntry.action` code in the current language;
 *  an action with no title key renders as the code itself (see `codeLabel`). */
export function auditActionLabel(action: string): string {
  const { titleKey } = auditActionMeta(action);
  return codeLabel(titleKey === null ? null : `dashboard:${titleKey}`, action);
}

/** Map an `AuditEntry.action` string to an icon + badge variant.
 *  `titleKey` is an i18n key under "dashboard:audit.action.*", or `null` for
 *  an unrecognised action. */
export function auditActionMeta(action: string): AuditIconMeta {
  const a = action.toLowerCase();

  // Account lifecycle
  if (a === "create_account" || a === "add_account")
    return {
      Icon: PlusCircle,
      variant: "ok",
      titleKey: "audit.action.createAccount",
    };
  if (a === "block_account" || a === "block")
    return {
      Icon: Ban,
      variant: "danger",
      titleKey: "audit.action.blockAccount",
    };
  if (a === "unblock_account" || a === "unblock")
    return {
      Icon: LockOpen,
      variant: "ok",
      titleKey: "audit.action.unblockAccount",
    };
  if (a === "update_account" || a === "edit_account")
    return {
      Icon: UserCog,
      variant: "accent",
      titleKey: "audit.action.updateAccount",
    };
  if (a === "delete_account")
    return {
      Icon: Trash2,
      variant: "danger",
      titleKey: "audit.action.deleteAccount",
    };

  // Group lifecycle
  if (a === "create_group" || a === "add_group")
    return {
      Icon: PlusCircle,
      variant: "ok",
      titleKey: "audit.action.createGroup",
    };
  if (a === "block_group")
    return {
      Icon: ShieldOff,
      variant: "danger",
      titleKey: "audit.action.blockGroup",
    };
  if (a === "unblock_group")
    return {
      Icon: LockOpen,
      variant: "ok",
      titleKey: "audit.action.unblockGroup",
    };
  if (a === "update_group" || a === "edit_group")
    return {
      Icon: Users,
      variant: "accent",
      titleKey: "audit.action.updateGroup",
    };
  if (a === "delete_group")
    return {
      Icon: Trash2,
      variant: "danger",
      titleKey: "audit.action.deleteGroup",
    };

  // Limits / policies
  if (a === "set_limit" || a === "create_limit" || a === "add_limit")
    return { Icon: Lock, variant: "warn", titleKey: "audit.action.setLimit" };
  if (a === "delete_limit" || a === "remove_limit")
    return {
      Icon: Trash2,
      variant: "danger",
      titleKey: "audit.action.deleteLimit",
    };
  if (a === "update_limit" || a === "edit_limit")
    return {
      Icon: Sliders,
      variant: "accent",
      titleKey: "audit.action.updateLimit",
    };

  // Adjustments / balances
  if (a === "adjustment" || a === "apply_adjustment")
    return {
      Icon: Coins,
      variant: "accent",
      titleKey: "audit.action.adjustment",
    };
  if (a === "balance" || a === "update_balance")
    return {
      Icon: CircleDollarSign,
      variant: "neutral",
      titleKey: "audit.action.balanceUpdate",
    };

  // Orders / trades
  if (a === "submit_drop_copy_order")
    return {
      Icon: AlertTriangle,
      variant: "danger",
      titleKey: "audit.action.dropCopyOrder",
    };
  if (a === "order" || a === "submit_order" || a === "create_order")
    return {
      Icon: ClipboardList,
      variant: "neutral",
      titleKey: "audit.action.order",
    };
  if (a === "trade" || a === "fill")
    return {
      Icon: ArrowLeftRight,
      variant: "ok",
      titleKey: "audit.action.tradeFill",
    };

  if (a === "reset_database")
    return {
      Icon: Trash2,
      variant: "danger",
      titleKey: "audit.action.resetDatabase",
    };
  if (a === "restart_service")
    return {
      Icon: RefreshCw,
      variant: "warn",
      titleKey: "audit.action.restartService",
    };
  if (a === "stop_service")
    return {
      Icon: Power,
      variant: "danger",
      titleKey: "audit.action.stopService",
    };

  // Policy / config and the rest: a title from the table, `null` if unknown.
  const titleKey = AUDIT_ACTION_TITLE_KEYS.get(a) ?? null;
  if (a.includes("policy") || a.includes("config"))
    return { Icon: Layers, variant: "accent", titleKey };

  return { Icon: HelpCircle, variant: "neutral", titleKey };
}
