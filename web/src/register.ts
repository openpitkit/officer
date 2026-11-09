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
  ShieldAlert,
  ShieldCheck,
  Skull,
  SlidersHorizontal,
  User,
  Users,
} from "lucide-react";

import { registerAppRowActions, registerAppVocabulary } from "@/appDefaults";
import {
  AboutNavEntry,
  LogoutNavEntry,
  NonReleaseNavEntry,
  RestartRequiredNavEntry,
  SensitiveDataNavEntry,
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

const defaultPages = [
  "dashboard",
  "accounts",
  "policies",
  "assets",
  "positions",
  "orders",
  "market-data",
  "trading-connections",
  "audit",
  "mcp-access",
  "signing-keys",
  "service",
] as const;

/** Stable ids accepted by registerOfficerDefaults. */
export type OfficerDefaultPageId = (typeof defaultPages)[number];

const pageRegistrations: Record<OfficerDefaultPageId, () => void> = {
  dashboard: () => {
    registerRoute({
      id: "dashboard",
      path: "/",
      order: 10,
      Component: Dashboard,
    });
    registerNav({
      id: "dashboard",
      to: "/",
      labelKey: "nav.dashboard",
      icon: LayoutDashboard,
      section: "primary",
      order: 10,
      end: true,
    });
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
  },
  accounts: () => {
    registerRoute({
      id: "accounts",
      path: "/accounts",
      order: 20,
      Component: Accounts,
    });
    registerPage({
      id: "accounts",
      titleKey: "accounts:account.title",
      Component: Account,
      when: () => !accountScope().showsAccounts,
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
    registerAppRowActions(["accounts"]);
  },
  policies: () => {
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
    registerNav({
      id: "policies",
      to: "/policies",
      labelKey: "nav.policies",
      icon: SlidersHorizontal,
      section: "primary",
      order: 50,
    });
    registerAppRowActions(["policies"]);
  },
  assets: () => {
    registerRoute({
      id: "assets",
      path: "/assets",
      order: 45,
      Component: Assets,
    });
    registerNav({
      id: "assets",
      to: "/assets",
      labelKey: "nav.assets",
      icon: BadgeDollarSign,
      section: "primary",
      order: 45,
    });
  },
  positions: () => {
    registerRoute({
      id: "positions",
      path: "/positions",
      order: 50,
      Component: Positions,
    });
    registerNav({
      id: "positions",
      to: "/positions",
      labelKey: "nav.positions",
      icon: BriefcaseBusiness,
      section: "primary",
      order: 30,
    });
  },
  orders: () => {
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
    registerNav({
      id: "orders",
      to: "/orders",
      labelKey: "nav.orders",
      icon: ArrowLeftRight,
      section: "primary",
      order: 40,
    });
  },
  "market-data": () => {
    registerRoute({
      id: "market-data",
      path: "/market-data",
      order: 80,
      Component: MarketData,
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
      id: "market-data",
      to: "/market-data",
      labelKey: "nav.marketData",
      icon: ChartCandlestick,
      section: "footer",
      order: 40,
    });
  },
  "trading-connections": () => {
    registerRoute({
      id: "trading-connections",
      path: "/trading-connections",
      order: 85,
      Component: TradingConnections,
    });
    registerNav({
      id: "trading-connections",
      to: "/trading-connections",
      labelKey: "nav.tradingConnections",
      icon: Route,
      section: "footer",
      order: 35,
    });
  },
  audit: () => {
    registerRoute({
      id: "audit",
      path: "/audit",
      order: 90,
      Component: Audit,
    });
    registerNav({
      id: "audit",
      to: "/audit",
      labelKey: "nav.audit",
      icon: ClipboardList,
      section: "footer",
      order: 10,
    });
  },
  "mcp-access": () => {
    registerRoute({
      id: "mcp-access",
      path: "/mcp-access",
      order: 100,
      Component: McpAccess,
    });
    registerNav({
      id: "mcp-access",
      to: "/mcp-access",
      labelKey: "nav.mcpAccess",
      icon: ShieldCheck,
      section: "footer",
      order: 20,
    });
  },
  "signing-keys": () => {
    registerRoute({
      id: "signing-keys",
      path: "/signing-keys",
      order: 110,
      Component: SigningKeys,
    });
    registerNav({
      id: "signing-keys",
      to: "/signing-keys",
      labelKey: "nav.signingKeys",
      icon: KeyRound,
      section: "footer",
      order: 30,
    });
  },
  service: () => {
    registerRoute({
      id: "service",
      path: "/service",
      order: 120,
      Component: Service,
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
      id: "sensitive-data",
      to: "/service#security",
      labelKey: "sensitiveData.title",
      icon: ShieldAlert,
      section: "footer",
      order: 55,
      render: () => createElement(SensitiveDataNavEntry),
    });
    registerNav({
      id: "non-release",
      to: "/service",
      labelKey: "brand.nonRelease",
      icon: Skull,
      section: "footer",
      order: 60,
      render: () => createElement(NonReleaseNavEntry),
    });
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
  },
};

/**
 * Register the open panel's routes, page overrides, navigation, widgets, row
 * actions, vocabulary, and locale resource map.
 * Importing the package initializes and registers nothing. Initialize i18next
 * first through initOfficerI18n() or the host's own init(), because this
 * function registers the locale resource map.
 * Select pages by id: dashboard, accounts, policies, assets, positions, orders,
 * market-data, trading-connections, audit, mcp-access, signing-keys, service.
 * Omit pages to register all defaults; [] registers nothing. Selection order
 * does not change the built-in ordering. Redirects belong to policies/orders,
 * account/group actions to accounts, limit actions to policies, and all widgets
 * to dashboard. Restart-required belongs to market-data; sensitive-data,
 * non-release, logout and about belong to service. Each nonempty selection
 * also registers the shared locale resources and vocabulary. Existing host
 * entries are not cleared; unknown ids throw before any registration.
 * A host calls this once before rendering, then reads stable ids through
 * getRoutes(), getNav(section), getPage(id), getWidgets(), getRowActions(scope),
 * getScopes(), and getPolicies(). It replaces entries by re-registering their
 * ids or removes them through the matching unregister API.
 */
export function registerOfficerDefaults(
  pages: readonly OfficerDefaultPageId[] = defaultPages,
): void {
  for (const page of pages) {
    if (!defaultPages.includes(page)) {
      throw new RangeError(`Unknown Officer default page: ${page}`);
    }
  }
  if (pages.length === 0) return;
  // Locale catalogs: en, ru, zh-CN, es, pt-BR, all namespaces from
  // src/i18n/locales.
  registerLocaleResourceMap(appLocaleResources);

  // Vocabulary ids include the generic rate-limit scopes and the semantic
  // underlying/settlement order-size scopes.
  registerAppVocabulary();

  for (const page of defaultPages) {
    if (pages.includes(page)) pageRegistrations[page]();
  }
}
