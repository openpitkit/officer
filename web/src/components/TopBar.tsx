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

import type { ReactNode } from "react";

import { Menu } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useSidebar } from "@/components/sidebar-context";
import { DisplaySwitches } from "@/components/DisplaySwitches";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";

interface TopBarProps {
  title: string;
  /** Optional trailing controls (refresh button, status pill, etc.). */
  actions?: ReactNode;
}

export function TopBar({ title, actions }: TopBarProps) {
  const { t } = useTranslation();
  const { toggle } = useSidebar();
  return (
    <header className="flex h-[var(--topbar-height)] shrink-0 items-center gap-2 overflow-hidden border-b border-border bg-surface px-[var(--dens-shell-x)] shadow-[inset_0_2px_0_var(--accent)]">
      <div className="flex min-w-[var(--dens-title-min)] shrink-0 items-center gap-2">
        <Button
          variant="outline"
          size="icon"
          className="md:hidden"
          aria-label={t("topBar.toggleNav")}
          onClick={toggle}
        >
          <Menu />
        </Button>
        <span className="hidden h-5 w-[3px] rounded-[1px] bg-accent sm:block" />
        <h1 className="min-w-0 truncate text-[length:var(--dens-title-fz)] font-bold tracking-tight text-text">
          {title}
        </h1>
      </div>
      <div className="ml-auto flex min-w-0 flex-1 flex-nowrap items-center justify-end gap-2 overflow-x-auto whitespace-nowrap md:flex-none md:shrink-0 md:overflow-visible">
        <DisplaySwitches />
        {actions}
        <LanguageSwitch />
        <ThemeSwitch />
      </div>
    </header>
  );
}
