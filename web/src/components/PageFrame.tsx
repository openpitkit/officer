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

import { useLayoutEffect, useRef, useState, type ReactNode } from "react";

import { Menu } from "lucide-react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import { useSidebar } from "@/components/sidebar-context";

export interface PageFrameProps {
  title: string;
  /**
   * Optional controls at the right of the top bar. When they overflow, the
   * frame raises the bar's compact level step by step, and the stylesheet
   * hides control labels per level.
   */
  controls?: ReactNode;
  children: ReactNode;
}

const MAX_COMPACT_LEVEL = 4;

/**
 * The frame of a panel screen: the top bar with the navigation toggle, the
 * title and the optional controls, and the scrolling content column below it.
 * Needs the sidebar context of `SidebarProvider`.
 */
export function PageFrame({ title, controls, children }: PageFrameProps) {
  const { t } = useTranslation();
  const { toggle } = useSidebar();
  const headerRef = useRef<HTMLElement | null>(null);
  const controlsRef = useRef<HTMLDivElement | null>(null);
  const [compactLevel, setCompactLevel] = useState(0);

  useLayoutEffect(() => {
    const header = headerRef.current;
    const controlsNode = controlsRef.current;
    if (!header || !controlsNode || typeof ResizeObserver === "undefined") {
      return;
    }

    let frame = 0;
    const fits = (level: number) => {
      header.dataset.topbarCompactLevel = String(level);
      return controlsNode.scrollWidth <= controlsNode.clientWidth + 1;
    };
    const measure = () => {
      window.cancelAnimationFrame(frame);
      frame = window.requestAnimationFrame(() => {
        const previous = Number(header.dataset.topbarCompactLevel || "0");
        let next = previous;
        while (next < MAX_COMPACT_LEVEL && !fits(next)) {
          next += 1;
        }
        while (next > 0 && fits(next - 1)) {
          next -= 1;
        }
        header.dataset.topbarCompactLevel = String(previous);
        setCompactLevel(next);
      });
    };

    const observer = new ResizeObserver(measure);
    observer.observe(header);
    observer.observe(controlsNode);
    measure();
    return () => {
      window.cancelAnimationFrame(frame);
      observer.disconnect();
    };
  }, [controls, title]);

  return (
    <div className="flex h-full min-h-0 flex-col">
      <header
        ref={headerRef}
        data-topbar-compact-level={compactLevel}
        className="ledger-masthead flex h-[var(--topbar-height)] shrink-0 items-center gap-2 overflow-hidden bg-bg px-[var(--dens-shell-x)]"
      >
        <div className="flex min-w-0 shrink-0 items-center gap-2 overflow-hidden">
          <Button
            variant="outline"
            size="icon"
            className="md:hidden"
            aria-label={t("topBar.toggleNav")}
            onClick={toggle}
          >
            <Menu />
          </Button>
          <span className="hidden h-5 w-px bg-accent sm:block" />
          <h1 className="ledger-title min-w-0 max-w-[40vw] truncate text-[length:var(--dens-title-fz)] font-bold text-text">
            {title}
          </h1>
        </div>
        {controls != null && (
          <div
            ref={controlsRef}
            className="topbar-actions ml-auto flex min-w-0 flex-1 flex-nowrap items-center justify-end gap-2 overflow-hidden whitespace-nowrap"
          >
            {controls}
          </div>
        )}
      </header>
      <main className="min-h-0 flex-1 overflow-y-auto bg-bg px-[var(--dens-page-pad)] py-[calc(var(--dens-page-pad)*0.75)]">
        <div className="mx-auto flex w-full max-w-[var(--content-max)] flex-col gap-y-[var(--dens-row-gap)]">
          {children}
        </div>
      </main>
    </div>
  );
}
