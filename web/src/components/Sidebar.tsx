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

import {
  ClipboardList,
  Coins,
  Database,
  LayoutDashboard,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  TrendingUp,
  Users,
  type LucideIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router-dom";

import { BrandMark } from "@/components/BrandMark";
import { useSidebar } from "@/components/sidebar-context";
import { useService } from "@/api/useService";
import { cn } from "@/lib/utils";

interface NavItem {
  to: string;
  labelKey: string;
  icon: LucideIcon;
  /** End-match so "/" is active only on the dashboard, not every route. */
  end: boolean;
}

const NAV: NavItem[] = [
  { to: "/", labelKey: "nav.dashboard", icon: LayoutDashboard, end: true },
  { to: "/accounts", labelKey: "nav.accounts", icon: Users, end: false },
  { to: "/positions", labelKey: "nav.positions", icon: Coins, end: false },
  { to: "/orders", labelKey: "nav.orders", icon: TrendingUp, end: false },
  { to: "/policies", labelKey: "nav.policies", icon: SlidersHorizontal, end: false },
];

// Secondary section pinned to the bottom, in display order.
const FOOTER: NavItem[] = [
  { to: "/audit", labelKey: "nav.audit", icon: ClipboardList, end: false },
  { to: "/mcp-access", labelKey: "nav.mcpAccess", icon: ShieldCheck, end: false },
  { to: "/market-data", labelKey: "nav.marketData", icon: Database, end: false },
  { to: "/service", labelKey: "nav.service", icon: Settings, end: false },
];

export function Sidebar() {
  const { t } = useTranslation();
  const { load } = useService();
  const { open, close } = useSidebar();
  const isNonRelease =
    load.state === "ready" && load.data.release === false;

  return (
    <>
      {/* Backdrop: mobile only, sits under the drawer (z-40 vs z-50). */}
      {open && (
        <div
          className="fixed inset-0 z-40 bg-black/50 md:hidden"
          onClick={close}
        />
      )}

      <aside
        className={cn(
          "fixed inset-y-0 left-0 z-50 w-56 shrink-0 transition-transform md:static md:translate-x-0",
          "flex flex-col border-r border-border bg-surface",
          open ? "translate-x-0" : "-translate-x-full",
        )}
      >
        <div className="flex items-center gap-2.5 border-b border-border px-4 py-4">
          <BrandMark className="h-6 w-6" />
          <div className="leading-tight">
            <div className="text-sm font-bold tracking-tight text-text">
              {t("brand.name")}
            </div>
            {isNonRelease && (
              <div className="text-[0.625rem] uppercase tracking-[0.12em] text-muted">
                {t("brand.nonRelease")}
              </div>
            )}
          </div>
        </div>

        <nav className="flex flex-1 flex-col gap-0.5 p-2">
          {NAV.map((item) => {
            const Icon = item.icon;
            return (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                onClick={close}
                className={({ isActive }) =>
                  cn(
                    "group flex items-center gap-2.5 rounded-card px-3 py-2 text-sm transition-colors",
                    isActive
                      ? "bg-accent-dim text-accent"
                      : "text-muted-lt hover:bg-accent-dim hover:text-accent",
                  )
                }
              >
                <Icon className="h-4 w-4 shrink-0" />
                <span className="flex-1 text-left">{t(item.labelKey)}</span>
              </NavLink>
            );
          })}
        </nav>

        <div className="border-t border-border p-2">
          {FOOTER.map((item) => {
            const Icon = item.icon;
            return (
              <NavLink
                key={item.to}
                to={item.to}
                end={item.end}
                onClick={close}
                className={({ isActive }) =>
                  cn(
                    "group flex items-center gap-2.5 rounded-card px-3 py-2 text-xs transition-colors",
                    isActive
                      ? "bg-accent-dim text-accent"
                      : "text-muted opacity-60 hover:bg-accent-dim hover:text-muted-lt hover:opacity-100",
                  )
                }
              >
                <Icon className="h-3.5 w-3.5 shrink-0" />
                <span className="flex-1 text-left">{t(item.labelKey)}</span>
              </NavLink>
            );
          })}
        </div>
      </aside>
    </>
  );
}
