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
          "fixed inset-y-0 left-0 z-50 w-[var(--sidebar-width)] shrink-0 transition-transform md:static md:translate-x-0",
          "flex flex-col border-r border-border bg-surface",
          open ? "translate-x-0" : "-translate-x-full",
        )}
      >
        <div className="flex min-h-[var(--topbar-height)] items-center gap-[var(--dens-nav-gap)] border-b border-border bg-bg/30 px-[var(--dens-shell-x)] py-[var(--dens-sidebar-brand-py)] shadow-[inset_0_2px_0_var(--accent)]">
          <BrandMark className="h-[var(--dens-brand-logo)] w-[var(--dens-brand-logo)]" />
          <div className="leading-tight">
            <div className="text-[length:var(--dens-brand-title-fz)] font-bold tracking-tight text-text">
              {t("brand.name")}
            </div>
            <div className="flex items-center gap-1.5 text-[length:var(--dens-brand-label-fz)] uppercase tracking-[0.12em] text-muted">
              <span>{t("brand.controlPlane")}</span>
              {isNonRelease && (
                <>
                  <span className="text-border">/</span>
                  <span>{t("brand.nonRelease")}</span>
                </>
              )}
            </div>
          </div>
        </div>

        <nav className="flex flex-1 flex-col gap-0.5 p-[var(--dens-sidebar-pad)]">
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
                    "group flex items-center gap-[var(--dens-nav-gap)] rounded-card px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-nav-fz)] transition-colors",
                    isActive
                      ? "bg-accent-dim text-accent"
                      : "text-muted-lt hover:bg-surface-hover hover:text-accent",
                  )
                }
              >
                <Icon className="h-[var(--dens-nav-icon)] w-[var(--dens-nav-icon)] shrink-0" />
                <span className="flex-1 text-left">{t(item.labelKey)}</span>
              </NavLink>
            );
          })}
        </nav>

        <div className="border-t border-border p-[var(--dens-sidebar-pad)]">
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
                    "group flex items-center gap-[var(--dens-nav-gap)] rounded-card px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] transition-colors",
                    isActive
                      ? "bg-accent-dim text-accent"
                      : "text-muted hover:bg-surface-hover hover:text-muted-lt",
                  )
                }
              >
                <Icon className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
                <span className="flex-1 text-left">{t(item.labelKey)}</span>
              </NavLink>
            );
          })}
        </div>
      </aside>
    </>
  );
}
