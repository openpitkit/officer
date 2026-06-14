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

import { Monitor, Moon, Sun } from "lucide-react";
import type { ComponentType } from "react";
import { useTranslation } from "react-i18next";

import { Button } from "@/components/ui/button";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { useTheme, type ThemeMode } from "@/theme/theme-context";

const MODES: {
  value: ThemeMode;
  labelKey: string;
  icon: ComponentType<{ className?: string }>;
}[] = [
  { value: "dark", labelKey: "theme.dark", icon: Moon },
  { value: "light", labelKey: "theme.light", icon: Sun },
  { value: "system", labelKey: "theme.system", icon: Monitor },
];

function isThemeMode(value: string): value is ThemeMode {
  return MODES.some((mode) => mode.value === value);
}

/**
 * Tri-state theme selector: dark / light / system, persisted in the browser.
 */
export function ThemeSwitch() {
  const { t } = useTranslation();
  const { mode, resolved, setMode } = useTheme();

  // The trigger glyph reflects the *effective* palette so the icon is honest in
  // system mode, while the menu shows the persisted preference.
  const TriggerIcon = resolved === "dark" ? Moon : Sun;
  const ariaLabel = t("theme.ariaLabel", { mode: t(`theme.${mode}`) });

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="outline"
          size="icon"
          className="shrink-0"
          aria-label={ariaLabel}
          title={ariaLabel}
        >
          <TriggerIcon className="h-4 w-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <DropdownMenuLabel>{t("theme.label")}</DropdownMenuLabel>
        <DropdownMenuSeparator />
        <DropdownMenuRadioGroup
          value={mode}
          onValueChange={(v) => {
            if (isThemeMode(v)) {
              setMode(v);
            }
          }}
        >
          {MODES.map(({ value, labelKey, icon: Icon }) => (
            <DropdownMenuRadioItem key={value} value={value}>
              <Icon className="h-3.5 w-3.5" />
              <span>{t(labelKey)}</span>
            </DropdownMenuRadioItem>
          ))}
        </DropdownMenuRadioGroup>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
