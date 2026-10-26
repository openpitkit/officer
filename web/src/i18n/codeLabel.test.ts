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

import { afterEach, describe, expect, it, vi } from "vitest";

import i18n from "@/i18n";
import { codeLabel } from "@/i18n/codeLabel";
import { LOCALE_CODES } from "@/i18n/locales";
import { ALL_ORDER_STATUSES } from "@/lib/orderStatus";
import { auditActionMeta } from "@/pages/dashboardActivity";

describe("codeLabel", () => {
  afterEach(async () => {
    vi.restoreAllMocks();
    await i18n.changeLanguage("en");
  });

  it("shows the catalog label of a known code", async () => {
    await i18n.changeLanguage("es");

    expect(codeLabel("orders:filter.status.rejected", "rejected")).toBe(
      "Rechazada",
    );
  });

  it("shows a code with no catalog entry as itself and warns once", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    expect(codeLabel("orders:filter.status.parked", "parked")).toBe("parked");
    expect(codeLabel("orders:filter.status.parked", "parked")).toBe("parked");
    expect(warn).toHaveBeenCalledTimes(1);
    expect(warn).toHaveBeenCalledWith(
      'No catalog label for code "parked" (orders:filter.status.parked); showing the code.',
    );
  });

  it("shows a code no mapping knows as itself and warns", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});

    expect(codeLabel(null, "future_action")).toBe("future_action");
    expect(warn).toHaveBeenCalledWith(
      'No catalog label for code "future_action"; showing the code.',
    );
  });
});

// The wire codes the panel labels, as the server defines them.
const SOURCES = ["panel", "api", "mcp", "system"];
const SIDES = ["buy", "sell"];
// framework/domain/entities.go, OrderEventType.
const EVENT_TYPES = [
  "submitted",
  "pre_trade_accepted",
  "pre_trade_rejected",
  "committed",
  "rolled_back",
  "confirmed",
  "fill",
  "commission",
  "cancelled",
];
const REJECT_SCOPES = ["order", "account"];
// framework/domain/reject_codes.go.
const REJECT_CODES = [
  "missing_required_field",
  "invalid_field_format",
  "invalid_field_value",
  "unsupported_order_type",
  "unsupported_time_in_force",
  "unsupported_order_attribute",
  "duplicate_client_order_id",
  "too_late_to_enter",
  "exchange_closed",
  "unknown_instrument",
  "unknown_account",
  "unknown_venue",
  "unknown_clearing_account",
  "unknown_collateral_asset",
  "insufficient_funds",
  "insufficient_margin",
  "insufficient_position",
  "credit_limit_exceeded",
  "risk_limit_exceeded",
  "order_exceeds_limit",
  "order_qty_exceeds_limit",
  "order_notional_exceeds_limit",
  "position_limit_exceeded",
  "concentration_limit_exceeded",
  "leverage_limit_exceeded",
  "rate_limit_exceeded",
  "pnl_kill_switch_triggered",
  "account_blocked",
  "account_not_authorized",
  "compliance_restriction",
  "instrument_restricted",
  "jurisdiction_restriction",
  "wash_trade_prevention",
  "self_match_prevention",
  "short_sale_restriction",
  "risk_configuration_missing",
  "reference_data_unavailable",
  "order_value_calculation_failed",
  "system_unavailable",
  "mark_price_unavailable",
  "account_adjustment_bounds_exceeded",
  "arithmetic_overflow",
  "custom",
  "other",
];
// framework/domain/domain.go, AuditAction.
const AUDIT_ACTIONS = [
  "hydrate",
  "create_asset",
  "update_asset",
  "delete_asset",
  "create_account",
  "update_account",
  "delete_account",
  "block",
  "unblock",
  "set_limit",
  "delete_limit",
  "set_group_notes",
  "block_group",
  "unblock_group",
  "set_notes",
  "set_group",
  "set_account_currency",
  "set_group_currency",
  "adjustment",
  "create_group",
  "update_group",
  "delete_group",
  "create_asset_class",
  "update_asset_class",
  "delete_asset_class",
  "submit_order",
  "submit_drop_copy_order",
  "execution_report",
  "set_mcp_access",
  "set_setting",
  "set_market_data",
  "set_trading",
  "venue_fee_skipped",
  "export_backup",
  "restore_backup",
  "export_business_csv",
  "reset_database",
  "restart_service",
  "stop_service",
  "generate_signing_key",
  "import_signing_key",
  "set_signing_config",
  "approval_issued",
  "approval_failed",
  "approval_confirmed",
  "approval_cancelled",
  "set_access_mode",
  "issue_access_token",
  "set_panel_password",
  "issue_recovery_code",
  "use_recovery_code",
];
// framework/backend/approval.go: the approval verdict, order type, recorded
// outcome and amount kind.
const VERDICTS = ["accept", "reject"];
const ORDER_TYPES = ["market", "limit"];
const OUTCOMES = ["applied", "confirmed", "cancelled"];
const AMOUNT_KINDS = ["quantity", "volume"];

const LABEL_KEYS = [
  ...ALL_ORDER_STATUSES.map((s) => `orders:filter.status.${s}`),
  ...SIDES.map((s) => `orders:filter.side.${s}`),
  ...SOURCES.map((s) => `common:source.${s}`),
  ...EVENT_TYPES.map((s) => `orders:detail.dialog.timeline.type.${s}`),
  ...REJECT_SCOPES.map((s) => `orders:detail.dialog.timeline.scopes.${s}`),
  ...REJECT_CODES.map((s) => `orders:check.rejectReasons.${s}`),
  ...VERDICTS.map((s) => `orders:breakdown.verdicts.${s}`),
  ...ORDER_TYPES.map((s) => `orders:breakdown.orderTypes.${s}`),
  ...OUTCOMES.map((s) => `orders:breakdown.outcomes.${s}`),
  ...AMOUNT_KINDS.map((s) => `orders:addOrder.dialog.amountKindOptions.${s}`),
  ...AUDIT_ACTIONS.map((action) => {
    const { titleKey } = auditActionMeta(action);
    return titleKey === null
      ? `no title key: ${action}`
      : `dashboard:${titleKey}`;
  }),
];

describe.each(LOCALE_CODES)("the %s catalog", (locale) => {
  it("labels every wire code the panel shows", () => {
    const unlabelled = LABEL_KEYS.filter((key) => {
      const [namespace, path] = key.split(":");
      const label: unknown = i18n.getResource(locale, namespace, path);
      return typeof label !== "string" || label === "";
    });

    expect(unlabelled).toEqual([]);
  });
});
