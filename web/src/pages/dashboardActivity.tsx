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
  /** Display label for the column header. */
  label: string;
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
  { label: string; route: "trading" | "audit" }
> = {
  trading:     { label: "Orders & Trades",  route: "trading" },
  accounts:    { label: "Accounts",          route: "audit"   },
  limits:      { label: "Limits",            route: "audit"   },
  groups:      { label: "Groups",            route: "audit"   },
  adjustments: { label: "Adjustments",       route: "audit"   },
  other:       { label: "Other",             route: "audit"   },
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
      label: BUCKET_META[b].label,
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
  title: string;
};

/** Map an `AuditEntry.action` string to an icon + badge variant. */
export function auditActionMeta(action: string): AuditIconMeta {
  const a = action.toLowerCase();

  // Account lifecycle
  if (a === "create_account" || a === "add_account")
    return { Icon: PlusCircle,       variant: "ok",      title: "Create account" };
  if (a === "block_account")
    return { Icon: Ban,              variant: "danger",  title: "Block account" };
  if (a === "unblock_account")
    return { Icon: LockOpen,         variant: "ok",      title: "Unblock account" };
  if (a === "update_account" || a === "edit_account")
    return { Icon: UserCog,          variant: "accent",  title: "Update account" };
  if (a === "delete_account")
    return { Icon: Trash2,           variant: "danger",  title: "Delete account" };

  // Group lifecycle
  if (a === "create_group" || a === "add_group")
    return { Icon: PlusCircle,       variant: "ok",      title: "Create group" };
  if (a === "block_group")
    return { Icon: ShieldOff,        variant: "danger",  title: "Block group" };
  if (a === "unblock_group")
    return { Icon: LockOpen,         variant: "ok",      title: "Unblock group" };
  if (a === "update_group" || a === "edit_group")
    return { Icon: Users,            variant: "accent",  title: "Update group" };
  if (a === "delete_group")
    return { Icon: Trash2,           variant: "danger",  title: "Delete group" };

  // Limits / policies
  if (a === "set_limit" || a === "create_limit" || a === "add_limit")
    return { Icon: Lock,             variant: "warn",    title: "Set limit" };
  if (a === "delete_limit" || a === "remove_limit")
    return { Icon: Trash2,           variant: "danger",  title: "Delete limit" };
  if (a === "update_limit" || a === "edit_limit")
    return { Icon: Sliders,          variant: "accent",  title: "Update limit" };

  // Adjustments / balances
  if (a === "adjustment" || a === "apply_adjustment")
    return { Icon: Coins,            variant: "accent",  title: "Adjustment" };
  if (a === "balance" || a === "update_balance")
    return { Icon: CircleDollarSign, variant: "neutral", title: "Balance update" };

  // Orders / trades
  if (a === "order" || a === "submit_order" || a === "create_order")
    return { Icon: ClipboardList,    variant: "neutral", title: "Order" };
  if (a === "trade" || a === "fill")
    return { Icon: ArrowLeftRight,   variant: "ok",      title: "Trade / fill" };

  // Policy / config
  if (a.includes("policy") || a.includes("config"))
    return { Icon: Layers,           variant: "accent",  title: action };

  return { Icon: HelpCircle,         variant: "neutral", title: action };
}
