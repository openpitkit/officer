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

import { createElement } from "react";
import {
  ArrowLeftRight,
  BadgeDollarSign,
  BriefcaseBusiness,
  ChartCandlestick,
  ClipboardList,
  Database,
  Info,
  KeyRound,
  LayoutDashboard,
  LogOut,
  Route,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  User,
  Users,
} from "lucide-react";

import { registerAppRowActions, registerAppVocabulary } from "@/appDefaults";
import {
  AboutNavEntry,
  LogoutNavEntry,
  RestartRequiredNavEntry,
} from "@/components/SidebarExtras";
import {
  registerLocaleResourceMap,
  registerNav,
  registerPage,
  registerRoute,
  registerWidget,
} from "@/framework";
import { appLocaleResources } from "@/i18n";
import { accountScope } from "@/lib/accountScope";
import { panelSession } from "@/lib/panelSession";
import { Account } from "@/pages/Account";
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
import { TradingConnections } from "@/pages/TradingConnections";
import {
  ActivityColumns,
  AuditStrip,
  CountsRow,
  MarketDataCard,
  McpAccessCard,
  TradingConnectionsCard,
} from "@/pages/dashboard/widgets";

// This module is the application's complete registration against the framework
// extension points. Stable ids let consumers replace entries by re-registering
// the id or remove entries through the matching unregister API.
export function registerOfficerDefaults(): void {
  // Locale catalogs: en, ru, zh-CN, es, pt-BR, all namespaces from
  // src/i18n/locales.
  registerLocaleResourceMap(appLocaleResources);

  // Vocabulary ids include the generic rate-limit scopes and the semantic
  // underlying/settlement order-size scopes.
  registerAppVocabulary();

  // Route ids: dashboard, accounts, policies, limits-redirect, assets,
  // positions, orders, trading-redirect, market-data, trading-connections,
  // audit, mcp-access, signing-keys, service.
  registerRoute({
    id: "dashboard",
    path: "/",
    order: 10,
    Component: Dashboard,
  });
  registerRoute({
    id: "accounts",
    path: "/accounts",
    order: 20,
    Component: Accounts,
  });
  registerRoute({
    id: "policies",
    path: "/policies",
    order: 30,
    Component: Limits,
  });
  registerRoute({
    id: "limits-redirect",
    path: "/limits",
    order: 40,
    redirectTo: "/policies",
  });
  registerRoute({
    id: "assets",
    path: "/assets",
    order: 45,
    Component: Assets,
  });
  registerRoute({
    id: "positions",
    path: "/positions",
    order: 50,
    Component: Positions,
  });
  registerRoute({
    id: "orders",
    path: "/orders",
    order: 60,
    Component: Orders,
  });
  registerRoute({
    id: "trading-redirect",
    path: "/trading",
    order: 70,
    redirectTo: "/orders",
  });
  registerRoute({
    id: "market-data",
    path: "/market-data",
    order: 80,
    Component: MarketData,
  });
  registerRoute({
    id: "trading-connections",
    path: "/trading-connections",
    order: 85,
    Component: TradingConnections,
  });
  registerRoute({
    id: "audit",
    path: "/audit",
    order: 90,
    Component: Audit,
  });
  registerRoute({
    id: "mcp-access",
    path: "/mcp-access",
    order: 100,
    Component: McpAccess,
  });
  registerRoute({
    id: "signing-keys",
    path: "/signing-keys",
    order: 110,
    Component: SigningKeys,
  });
  registerRoute({
    id: "service",
    path: "/service",
    order: 120,
    Component: Service,
  });

  // Page ids: accounts (the one account's page while accounts are hidden).
  registerPage({
    id: "accounts",
    titleKey: "accounts:account.title",
    Component: Account,
    when: () => !accountScope().showsAccounts,
  });

  // Nav ids: dashboard, accounts, account, positions, orders, policies,
  // restart-required, audit, mcp-access, signing-keys, trading-connections,
  // market-data, service, non-release, logout, about.
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
    when: () => accountScope().showsAccounts,
  });
  registerNav({
    id: "account",
    to: "/accounts",
    labelKey: "nav.account",
    icon: User,
    section: "primary",
    order: 20,
    when: () => !accountScope().showsAccounts,
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
    id: "trading-connections",
    to: "/trading-connections",
    labelKey: "nav.tradingConnections",
    icon: Route,
    section: "footer",
    order: 35,
  });
  registerNav({
    id: "market-data",
    to: "/market-data",
    labelKey: "nav.marketData",
    icon: ChartCandlestick,
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
  // The sidebar carries no NON-RELEASE badge.
  registerNav({
    id: "logout",
    to: "#logout",
    labelKey: "nav.logOut",
    icon: LogOut,
    section: "footer",
    order: 65,
    when: () => panelSession().accessMode === "token",
    render: () => createElement(LogoutNavEntry),
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

  // Widget ids: counts-row, mcp-access-card, trading-connections-card,
  // market-data-card, activity-columns, audit-strip.
  registerWidget({ id: "counts-row", order: 10, Component: CountsRow });
  registerWidget({
    id: "mcp-access-card",
    order: 20,
    Component: McpAccessCard,
  });
  registerWidget({
    id: "trading-connections-card",
    order: 25,
    Component: TradingConnectionsCard,
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
  registerAppRowActions();
}

registerOfficerDefaults();
