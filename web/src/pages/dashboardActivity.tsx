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

// Dashboard-local helpers: activity grouping + audit action icons.

import {
  ArrowLeftRight,
  Ban,
  CircleDollarSign,
  ClipboardList,
  Coins,
  HelpCircle,
  Layers,
  Lock,
  LockOpen,
  PlusCircle,
  ShieldOff,
  Sliders,
  Trash2,
  UserCog,
  Users,
} from "lucide-react";
import type { ComponentType } from "react";

import type { ActivityEntry } from "@/api/types";

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
function kindBucket(kind: string): "trading" | "accounts" | "limits" | "groups" | "adjustments" | "other" {
  const k = kind.toLowerCase();
  if (k.startsWith("order") || k.startsWith("trade") || k.startsWith("fill") || k.startsWith("exec")) {
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
  if (k.startsWith("adjustment") || k.startsWith("balance") || k.startsWith("fund")) {
    return "adjustments";
  }
  return "other";
}

const BUCKET_META: Record<
  ReturnType<typeof kindBucket>,
  { labelKey: string; route: "trading" | "audit" }
> = {
  trading:     { labelKey: "activity.bucket.trading",     route: "trading" },
  accounts:    { labelKey: "activity.bucket.accounts",    route: "audit"   },
  limits:      { labelKey: "activity.bucket.limits",      route: "audit"   },
  groups:      { labelKey: "activity.bucket.groups",      route: "audit"   },
  adjustments: { labelKey: "activity.bucket.adjustments", route: "audit"   },
  other:       { labelKey: "activity.bucket.other",       route: "audit"   },
};

/** Bucket ordering — determines column order. */
const BUCKET_ORDER: ReturnType<typeof kindBucket>[] = [
  "trading", "accounts", "adjustments", "limits", "groups", "other",
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
  return BUCKET_ORDER
    .filter((b) => map.has(b))
    .map((b) => ({
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
  /** i18n key under the "dashboard" namespace, or a raw fallback string. */
  titleKey: string;
};

/** Map an `AuditEntry.action` string to an icon + badge variant.
 *  `titleKey` is an i18n key under "dashboard:audit.action.*", or the raw
 *  action string for unrecognised actions (used as i18n defaultValue). */
export function auditActionMeta(action: string): AuditIconMeta {
  const a = action.toLowerCase();

  // Account lifecycle
  if (a === "create_account" || a === "add_account")
    return { Icon: PlusCircle,       variant: "ok",      titleKey: "audit.action.createAccount" };
  if (a === "block_account")
    return { Icon: Ban,              variant: "danger",  titleKey: "audit.action.blockAccount" };
  if (a === "unblock_account")
    return { Icon: LockOpen,         variant: "ok",      titleKey: "audit.action.unblockAccount" };
  if (a === "update_account" || a === "edit_account")
    return { Icon: UserCog,          variant: "accent",  titleKey: "audit.action.updateAccount" };
  if (a === "delete_account")
    return { Icon: Trash2,           variant: "danger",  titleKey: "audit.action.deleteAccount" };

  // Group lifecycle
  if (a === "create_group" || a === "add_group")
    return { Icon: PlusCircle,       variant: "ok",      titleKey: "audit.action.createGroup" };
  if (a === "block_group")
    return { Icon: ShieldOff,        variant: "danger",  titleKey: "audit.action.blockGroup" };
  if (a === "unblock_group")
    return { Icon: LockOpen,         variant: "ok",      titleKey: "audit.action.unblockGroup" };
  if (a === "update_group" || a === "edit_group")
    return { Icon: Users,            variant: "accent",  titleKey: "audit.action.updateGroup" };
  if (a === "delete_group")
    return { Icon: Trash2,           variant: "danger",  titleKey: "audit.action.deleteGroup" };

  // Limits / policies
  if (a === "set_limit" || a === "create_limit" || a === "add_limit")
    return { Icon: Lock,             variant: "warn",    titleKey: "audit.action.setLimit" };
  if (a === "delete_limit" || a === "remove_limit")
    return { Icon: Trash2,           variant: "danger",  titleKey: "audit.action.deleteLimit" };
  if (a === "update_limit" || a === "edit_limit")
    return { Icon: Sliders,          variant: "accent",  titleKey: "audit.action.updateLimit" };

  // Adjustments / balances
  if (a === "adjustment" || a === "apply_adjustment")
    return { Icon: Coins,            variant: "accent",  titleKey: "audit.action.adjustment" };
  if (a === "balance" || a === "update_balance")
    return { Icon: CircleDollarSign, variant: "neutral", titleKey: "audit.action.balanceUpdate" };

  // Orders / trades
  if (a === "order" || a === "submit_order" || a === "create_order")
    return { Icon: ClipboardList,    variant: "neutral", titleKey: "audit.action.order" };
  if (a === "trade" || a === "fill")
    return { Icon: ArrowLeftRight,   variant: "ok",      titleKey: "audit.action.tradeFill" };

  if (a === "reset_database")
    return { Icon: Trash2,           variant: "danger",  titleKey: "audit.action.resetDatabase" };

  // Policy / config and unknown — raw action string as fallback key.
  if (a.includes("policy") || a.includes("config"))
    return { Icon: Layers,           variant: "accent",  titleKey: action };

  return { Icon: HelpCircle,         variant: "neutral", titleKey: action };
}
