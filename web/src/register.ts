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

import { createElement } from "react";
import {
  ArrowLeftRight,
  BadgeDollarSign,
  BriefcaseBusiness,
  ClipboardList,
  Database,
  Info,
  KeyRound,
  LayoutDashboard,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  Users,
} from "lucide-react";

import {
  AboutNavEntry,
  NonReleaseNavEntry,
  RestartRequiredNavEntry,
} from "@/components/SidebarExtras";
import {
  registerLocaleResourceMap,
  registerNav,
  registerRoute,
  registerWidget,
} from "@/framework";
import { openLocaleResources } from "@/i18n";
import { registerOpenRowActions, registerOpenVocabulary } from "@/openDefaults";
import { openRoutes } from "@/openRoutes";
import { Accounts } from "@/pages/Accounts";
import { Assets } from "@/pages/Assets";
import { Audit } from "@/pages/Audit";
import { Dashboard } from "@/pages/Dashboard";
import { Limits } from "@/pages/Limits";
import { MarketData } from "@/pages/MarketData";
import { McpAccess } from "@/pages/McpAccess";
import { Orders } from "@/pages/Orders";
import { Positions } from "@/pages/Positions";
import { Service } from "@/pages/Service";
import { SigningKeys } from "@/pages/SigningKeys";
import {
  ActivityColumns,
  AuditStrip,
  CountsRow,
  MarketDataCard,
  McpAccessCard,
} from "@/pages/dashboard/widgets";

const openRouteComponents = {
  dashboard: Dashboard,
  accounts: Accounts,
  assets: Assets,
  policies: Limits,
  positions: Positions,
  orders: Orders,
  "market-data": MarketData,
  audit: Audit,
  "mcp-access": McpAccess,
  "signing-keys": SigningKeys,
  service: Service,
} as const;

// This module is the open product's complete registration against the framework
// extension points. Stable ids let consumers replace entries by re-registering
// the id or remove entries through the matching unregister API.
export function registerOpenOfficerDefaults(): void {
  // Locale catalogs: en, ru, zh-CN, all namespaces from src/i18n/locales.
  registerLocaleResourceMap(openLocaleResources);

  // Vocabulary ids: broker, asset, account, account_asset, rate_limit,
  // order_size_limit.
  registerOpenVocabulary();

  // Route ids: dashboard, accounts, policies, limits-redirect, positions,
  // orders, trading-redirect, market-data, audit, mcp-access, signing-keys,
  // service.
  for (const route of openRoutes) {
    if ("redirectTo" in route) {
      registerRoute(route);
      continue;
    }
    registerRoute({
      ...route,
      Component: openRouteComponents[route.id],
    });
  }

  // Nav ids: dashboard, accounts, positions, orders, policies,
  // restart-required, audit, mcp-access, signing-keys, market-data, service,
  // non-release, about.
  registerNav({
    id: "dashboard",
    to: "/",
    labelKey: "nav.dashboard",
    icon: LayoutDashboard,
    section: "primary",
    order: 10,
    end: true,
  });
  registerNav({
    id: "accounts",
    to: "/accounts",
    labelKey: "nav.accounts",
    icon: Users,
    section: "primary",
    order: 20,
  });
  registerNav({
    id: "positions",
    to: "/positions",
    labelKey: "nav.positions",
    icon: BriefcaseBusiness,
    section: "primary",
    order: 30,
  });
  registerNav({
    id: "orders",
    to: "/orders",
    labelKey: "nav.orders",
    icon: ArrowLeftRight,
    section: "primary",
    order: 40,
  });
  registerNav({
    id: "assets",
    to: "/assets",
    labelKey: "nav.assets",
    icon: BadgeDollarSign,
    section: "primary",
    order: 45,
  });
  registerNav({
    id: "policies",
    to: "/policies",
    labelKey: "nav.policies",
    icon: SlidersHorizontal,
    section: "primary",
    order: 50,
  });
  registerNav({
    id: "restart-required",
    to: "/market-data",
    labelKey: "nav.restartRequired",
    icon: Database,
    section: "footer",
    order: 5,
    render: () => createElement(RestartRequiredNavEntry),
  });
  registerNav({
    id: "audit",
    to: "/audit",
    labelKey: "nav.audit",
    icon: ClipboardList,
    section: "footer",
    order: 10,
  });
  registerNav({
    id: "mcp-access",
    to: "/mcp-access",
    labelKey: "nav.mcpAccess",
    icon: ShieldCheck,
    section: "footer",
    order: 20,
  });
  registerNav({
    id: "signing-keys",
    to: "/signing-keys",
    labelKey: "nav.signingKeys",
    icon: KeyRound,
    section: "footer",
    order: 30,
  });
  registerNav({
    id: "market-data",
    to: "/market-data",
    labelKey: "nav.marketData",
    icon: Database,
    section: "footer",
    order: 40,
  });
  registerNav({
    id: "service",
    to: "/service",
    labelKey: "nav.service",
    icon: Settings,
    section: "footer",
    order: 50,
  });
  registerNav({
    id: "non-release",
    to: "/service",
    labelKey: "brand.nonRelease",
    icon: Settings,
    section: "footer",
    order: 60,
    render: () => createElement(NonReleaseNavEntry),
  });
  registerNav({
    id: "about",
    to: "#about",
    labelKey: "about.title",
    icon: Info,
    section: "footer",
    order: 70,
    render: () => createElement(AboutNavEntry),
  });

  // Widget ids: counts-row, mcp-access-card, market-data-card,
  // activity-columns, audit-strip.
  registerWidget({ id: "counts-row", order: 10, Component: CountsRow });
  registerWidget({
    id: "mcp-access-card",
    order: 20,
    Component: McpAccessCard,
  });
  registerWidget({
    id: "market-data-card",
    order: 30,
    Component: MarketDataCard,
  });
  registerWidget({
    id: "activity-columns",
    order: 40,
    Component: ActivityColumns,
  });
  registerWidget({ id: "audit-strip", order: 50, Component: AuditStrip });

  // Row action ids: account-positions, account-trading, account-policies,
  // account-audit, account-delete, group-delete, limit-edit, limit-delete.
  registerOpenRowActions();
}

registerOpenOfficerDefaults();
