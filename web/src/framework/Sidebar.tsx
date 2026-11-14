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

import { Fragment } from "react";
import { useTranslation } from "react-i18next";
import { NavLink } from "react-router-dom";

import { BrandLockup } from "../components/BrandLockup";
import { Button } from "../components/ui/button";
import { PanelLeftClose, PanelLeftOpen } from "lucide-react";
import { useSidebar } from "../components/sidebar-context";
import { useHasPermission } from "./auth/auth-context";
import { getNav, type NavEntry } from "./registries/nav";
import { cn } from "../lib/utils";

function StandardNavEntry({
  entry,
  footer,
}: {
  entry: NavEntry;
  footer: boolean;
}) {
  const { t } = useTranslation();
  const { close, collapsed } = useSidebar();
  const Icon = entry.icon;
  return (
    <NavLink
      to={entry.to}
      end={entry.end}
      onClick={close}
      title={collapsed ? t(entry.labelKey) : undefined}
      className={({ isActive }) =>
        cn(
          "group flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] transition-colors",
          collapsed && "justify-center gap-0 px-2",
          footer
            ? "text-[length:var(--dens-footer-fz)]"
            : "text-[length:var(--dens-nav-fz)]",
          isActive
            ? "bg-accent-dim text-accent"
            : footer
              ? "text-muted hover:bg-surface-hover hover:text-muted-lt"
              : "text-muted-lt hover:bg-surface-hover hover:text-accent",
        )
      }
    >
      <Icon
        className={cn(
          "shrink-0",
          footer
            ? "h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)]"
            : "h-[var(--dens-nav-icon)] w-[var(--dens-nav-icon)]",
        )}
      />
      <span className={collapsed ? "sr-only" : "flex-1 text-left"}>
        {t(entry.labelKey)}
      </span>
    </NavLink>
  );
}

function renderEntry(entry: NavEntry, footer: boolean) {
  if (!(entry.when?.() ?? true)) {
    return null;
  }
  if (entry.render) {
    return <Fragment key={entry.id}>{entry.render()}</Fragment>;
  }
  return <StandardNavEntry key={entry.id} entry={entry} footer={footer} />;
}

export function Sidebar() {
  const { t } = useTranslation();
  const { open, close, collapsed, toggleCollapsed } = useSidebar();
  const hasPermission = useHasPermission();

  return (
    <>
      {open && (
        <div
          className="fixed inset-0 z-40 bg-black/50 md:hidden"
          onClick={close}
        />
      )}

      <aside
        data-collapsed={collapsed}
        className={cn(
          "fixed inset-y-0 left-0 z-50 shrink-0 transition-[width,transform] md:static md:translate-x-0",
          collapsed ? "w-16" : "w-[var(--sidebar-width)]",
          "flex min-h-0 flex-col border-r border-border bg-surface",
          open ? "translate-x-0" : "-translate-x-full",
        )}
      >
        <div
          className={cn(
            "ledger-masthead flex h-[var(--topbar-height)] shrink-0 items-center overflow-hidden bg-bg/30",
            collapsed
              ? "justify-center gap-1 px-1"
              : "gap-2 px-[var(--dens-shell-x)]",
          )}
        >
          {!collapsed && <BrandLockup />}
          <Button
            type="button"
            variant="ghost"
            size="icon"
            className={cn("h-7 w-7 shrink-0", !collapsed && "ml-auto")}
            title={t(collapsed ? "nav.expandSidebar" : "nav.collapseSidebar")}
            aria-label={t(
              collapsed ? "nav.expandSidebar" : "nav.collapseSidebar",
            )}
            aria-expanded={!collapsed}
            onClick={toggleCollapsed}
          >
            {collapsed ? <PanelLeftOpen /> : <PanelLeftClose />}
          </Button>
        </div>

        <div className="flex min-h-0 flex-1 flex-col">
          <nav
            className={cn(
              "sidebar-scroll flex min-h-0 flex-1 flex-col gap-0.5 overflow-x-hidden overflow-y-auto",
              collapsed ? "p-1" : "p-[var(--dens-sidebar-pad)]",
            )}
          >
            {getNav("primary").map((entry) =>
              entry.permission && !hasPermission(entry.permission)
                ? null
                : renderEntry(entry, false),
            )}
          </nav>

          <div
            className={cn(
              "sidebar-scroll min-h-0 max-h-[66.666667%] shrink-0 overflow-x-hidden overflow-y-auto border-t border-border",
              collapsed ? "p-1" : "p-[var(--dens-sidebar-pad)]",
            )}
          >
            {getNav("footer").map((entry) =>
              entry.permission && !hasPermission(entry.permission)
                ? null
                : renderEntry(entry, true),
            )}
          </div>
        </div>
      </aside>
    </>
  );
}
