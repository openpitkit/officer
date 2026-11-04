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

import type { ReactNode } from "react";

import { DisplaySwitches } from "@/components/DisplaySwitches";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";

interface TopBarControlsProps {
  /** Optional page controls (refresh button, status pill, etc.). */
  actions?: ReactNode;
}

/** The panel's top bar controls: display, page, language and theme. */
export function TopBarControls({ actions }: TopBarControlsProps) {
  return (
    <>
      <DisplaySwitches />
      <div className="topbar-page-actions flex shrink-0 items-center gap-2 [&_button]:max-sm:w-[var(--dens-button-icon)] [&_button]:max-sm:overflow-hidden [&_button]:max-sm:px-0 [&_button]:max-sm:text-[0px]">
        {actions}
      </div>
      <div className="topbar-language hidden shrink-0 sm:block">
        <LanguageSwitch />
      </div>
      <ThemeSwitch />
    </>
  );
}
