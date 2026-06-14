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
  AlignJustify,
  Palette,
  Rows2,
  Rows3,
  type LucideIcon,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import {
  useDisplayPreferences,
  type DensityMode,
  type TradeStyle,
} from "@/theme/display-context";
import { cn } from "@/lib/utils";

const DENSITY_OPTIONS: {
  value: DensityMode;
  labelKey: string;
  icon: LucideIcon;
}[] = [
  { value: "comfortable", labelKey: "display.density.comfortable", icon: Rows2 },
  { value: "compact", labelKey: "display.density.compact", icon: Rows3 },
  { value: "terminal", labelKey: "display.density.terminal", icon: AlignJustify },
];

const TRADE_STYLE_OPTIONS: {
  value: TradeStyle;
  labelKey: string;
  titleKey: string;
  swatchClassName: string;
}[] = [
  {
    value: "tape",
    labelKey: "display.tradeStyle.tape",
    titleKey: "display.tradeStyle.tapeTitle",
    swatchClassName: "bg-[#3fb950]",
  },
  {
    value: "ladder",
    labelKey: "display.tradeStyle.ladder",
    titleKey: "display.tradeStyle.ladderTitle",
    swatchClassName: "bg-[#4d9fff]",
  },
];

export function DisplaySwitches({ className }: { className?: string }) {
  const { t } = useTranslation();
  const { density, tradeStyle, setDensity, setTradeStyle } =
    useDisplayPreferences();

  return (
    <div
      className={cn(
        "display-switches flex shrink-0 items-center gap-0.5 sm:gap-2",
        className,
      )}
    >
      <div
        className="display-switches__density inline-flex h-[var(--dens-control-h)] shrink-0 overflow-hidden rounded-card border border-border bg-bg/70 shadow-[inset_0_1px_0_rgba(255,255,255,0.03)]"
        role="group"
        aria-label={t("display.density.ariaLabel")}
        title={t("display.density.label")}
      >
        {DENSITY_OPTIONS.map(({ value, labelKey, icon: Icon }) => (
          <button
            key={value}
            type="button"
            title={t(labelKey)}
            className={cn(
              "inline-flex h-[var(--dens-control-h)] items-center gap-[var(--dens-control-gap)] border-l border-border px-1 text-[length:var(--dens-control-fz)] font-medium transition-colors duration-[180ms] first:border-l-0 sm:px-[var(--dens-control-px)]",
              density === value
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-accent",
            )}
            aria-pressed={density === value}
            onClick={() => setDensity(value)}
          >
            <Icon className="h-[var(--dens-control-icon)] w-[var(--dens-control-icon)]" />
            <span className="display-switches__density-label">
              {t(labelKey)}
            </span>
          </button>
        ))}
      </div>

      <div
        className="display-switches__trade inline-flex h-[var(--dens-control-h)] shrink-0 overflow-hidden rounded-card border border-border bg-bg/70 shadow-[inset_0_1px_0_rgba(255,255,255,0.03)]"
        role="group"
        aria-label={t("display.tradeStyle.ariaLabel")}
        title={t("display.tradeStyle.label")}
      >
        <div className="display-switches__trade-heading inline-flex h-[var(--dens-control-h)] items-center gap-[var(--dens-control-gap)] border-r border-border px-[var(--dens-control-px)] text-[length:var(--dens-control-fz)] font-bold uppercase tracking-[0.07em] text-muted">
          <Palette className="h-[var(--dens-control-icon)] w-[var(--dens-control-icon)]" />
          <span className="display-switches__trade-heading-label">
            {t("display.tradeStyle.label")}
          </span>
        </div>
        {TRADE_STYLE_OPTIONS.map(({ value, labelKey, titleKey, swatchClassName }) => (
          <button
            key={value}
            type="button"
            title={t(titleKey)}
            className={cn(
              "inline-flex h-[var(--dens-control-h)] items-center gap-[var(--dens-control-gap)] border-l border-border px-1 text-[length:var(--dens-control-fz)] font-medium transition-colors duration-[180ms] first:border-l-0 sm:px-[var(--dens-control-px)]",
              tradeStyle === value
                ? "bg-accent-dim text-accent"
                : "text-muted-lt hover:bg-surface-hover hover:text-accent",
            )}
            aria-pressed={tradeStyle === value}
            onClick={() => setTradeStyle(value)}
          >
            <span
              className={cn(
                "h-[var(--dens-swatch)] w-[var(--dens-swatch)] rounded-[2px] shadow-[0_0_0_1px_var(--tag-border)]",
                swatchClassName,
              )}
            />
            <span className="display-switches__trade-label">
              {t(labelKey)}
            </span>
          </button>
        ))}
      </div>
    </div>
  );
}
