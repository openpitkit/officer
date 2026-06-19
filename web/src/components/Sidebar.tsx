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
  AlertTriangle,
  ClipboardList,
  Coins,
  Database,
  ExternalLink,
  Info,
  KeyRound,
  LayoutDashboard,
  Settings,
  ShieldCheck,
  SlidersHorizontal,
  TrendingUp,
  Users,
  type LucideIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link, NavLink } from "react-router-dom";

import { useMarketData } from "@/api/useMarketData";
import { useService } from "@/api/useService";
import { BrandMark } from "@/components/BrandMark";
import { useSidebar } from "@/components/sidebar-context";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
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
  { to: "/signing-keys", labelKey: "nav.signingKeys", icon: KeyRound, end: false },
  { to: "/market-data", labelKey: "nav.marketData", icon: Database, end: false },
  { to: "/service", labelKey: "nav.service", icon: Settings, end: false },
];

export function Sidebar() {
  const { t } = useTranslation();
  const { load } = useService();
  const { load: marketDataLoad } = useMarketData();
  const { open, close } = useSidebar();
  const isNonRelease =
    load.state === "ready" && load.data.release === false;
  const restartRequired =
    marketDataLoad.state === "ready" && marketDataLoad.data.restartRequired;
  const aboutLinks = [
    {
      label: t("about.links.website"),
      href: "https://officer.openpit.dev?officer",
    },
    {
      label: t("about.links.issues"),
      href: "https://github.com/openpitkit/officer/issues?officer",
    },
    {
      label: t("about.links.discussions"),
      href: "https://github.com/openpitkit/officer/discussions?officer",
    },
    {
      label: t("about.links.openpit"),
      href: "https://openpit.dev?officer",
    },
  ];

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
        <div className="ledger-masthead flex h-[var(--topbar-height)] shrink-0 items-center gap-[var(--dens-nav-gap)] overflow-hidden bg-bg/30 px-[var(--dens-shell-x)]">
          <BrandMark className="h-[var(--dens-brand-logo)] w-[var(--dens-brand-logo)]" />
          <div className="min-w-0 leading-tight">
            <div className="text-[length:var(--dens-brand-title-fz)] font-bold tracking-tight text-text">
              {t("brand.name")}
            </div>
            <div className="text-[length:var(--dens-brand-label-fz)] uppercase tracking-[0.12em] text-muted">
              {t("brand.controlPlane")}
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
                    "group flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-nav-fz)] transition-colors",
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
          {restartRequired && (
            <Link
              to="/market-data"
              onClick={close}
              className="mb-1 flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] border border-[var(--warn)]/40 bg-[var(--warn)]/10 px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] font-semibold text-[var(--warn)] transition-colors hover:bg-[var(--warn)]/15"
            >
              <AlertTriangle className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
              <span className="flex-1 text-left">
                {t("nav.restartRequired")}
              </span>
            </Link>
          )}
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
                    "group flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] transition-colors",
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
          {isNonRelease && (
            <Link
              to="/service"
              onClick={close}
              className="mt-1 flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] border border-accent bg-accent px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] font-bold uppercase tracking-[0.07em] text-bg transition-colors hover:border-accent-2 hover:bg-accent-2"
            >
              <AlertTriangle className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
              <span className="flex-1 text-left">{t("brand.nonRelease")}</span>
            </Link>
          )}
          <Dialog>
            <DialogTrigger asChild>
              <button
                type="button"
                className="mt-1 flex w-full items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-left text-[length:var(--dens-footer-fz)] text-muted transition-colors hover:bg-surface-hover hover:text-muted-lt"
              >
                <Info className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
                <span className="flex-1">{t("about.title")}</span>
              </button>
            </DialogTrigger>
            <DialogContent className="max-w-xl">
              <DialogHeader>
                <DialogTitle>{t("about.title")}</DialogTitle>
                <DialogDescription>{t("about.description")}</DialogDescription>
              </DialogHeader>
              <div className="space-y-4 text-sm">
                <div className="rounded-card border border-border bg-surface-2 p-3">
                  <div className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                    {t("about.product")}
                  </div>
                  <div className="mt-1 text-text">{t("brand.name")}</div>
                </div>
                <div className="rounded-card border border-border bg-surface-2 p-3">
                  <div className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
                    {t("about.copyrightLabel")}
                  </div>
                  <div className="mt-1 text-text">{t("about.copyright")}</div>
                </div>
                <div className="grid gap-2 sm:grid-cols-2">
                  {aboutLinks.map((link) => (
                    <a
                      key={link.href}
                      href={link.href}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center justify-between gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt transition-colors hover:border-border-hover hover:bg-card-hover-bg hover:text-accent"
                    >
                      <span>{link.label}</span>
                      <ExternalLink className="h-3.5 w-3.5 shrink-0" />
                    </a>
                  ))}
                </div>
              </div>
            </DialogContent>
          </Dialog>
        </div>
      </aside>
    </>
  );
}
