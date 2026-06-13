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
import { NavLink } from "react-router-dom";

import { BrandMark } from "@/components/BrandMark";
import { useService } from "@/api/useService";
import { cn } from "@/lib/utils";

interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
  /** End-match so "/" is active only on the dashboard, not every route. */
  end: boolean;
}

const NAV: NavItem[] = [
  { to: "/", label: "Dashboard", icon: LayoutDashboard, end: true },
  { to: "/accounts", label: "Accounts", icon: Users, end: false },
  { to: "/positions", label: "Positions", icon: Coins, end: false },
  { to: "/orders", label: "Orders", icon: TrendingUp, end: false },
  { to: "/policies", label: "Policies", icon: SlidersHorizontal, end: false },
];

// Secondary section pinned to the bottom, in display order.
const FOOTER: NavItem[] = [
  { to: "/audit", label: "Audit", icon: ClipboardList, end: false },
  { to: "/mcp-access", label: "MCP access", icon: ShieldCheck, end: false },
  { to: "/market-data", label: "Market Data", icon: Database, end: false },
  { to: "/service", label: "Service", icon: Settings, end: false },
];

export function Sidebar() {
  const { load } = useService();
  const isNonRelease =
    load.state === "ready" && load.data.release === false;

  return (
    <aside className="flex w-56 shrink-0 flex-col border-r border-border bg-surface">
      <div className="flex items-center gap-2.5 border-b border-border px-4 py-4">
        <BrandMark className="h-6 w-6" />
        <div className="leading-tight">
          <div className="text-sm font-bold tracking-tight text-text">
            Pit Officer
          </div>
          {isNonRelease && (
            <div className="text-[0.625rem] uppercase tracking-[0.12em] text-muted">
              non-release
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
              <span className="flex-1 text-left">{item.label}</span>
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
              className={({ isActive }) =>
                cn(
                  "group flex items-center gap-2.5 rounded-card px-3 py-2 text-xs transition-colors",
                  isActive
                    ? "bg-accent-dim text-muted"
                    : "text-muted hover:bg-accent-dim hover:text-muted-lt",
                )
              }
            >
              <Icon className="h-3.5 w-3.5 shrink-0 opacity-60" />
              <span className="flex-1 text-left opacity-60">{item.label}</span>
            </NavLink>
          );
        })}
      </div>
    </aside>
  );
}
