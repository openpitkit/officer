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

import { BrandLockup } from "@/components/BrandLockup";
import { LanguageSwitch } from "@/components/LanguageSwitch";
import { ThemeSwitch } from "@/components/ThemeSwitch";

/**
 * The top bar of a screen shown outside the panel shell: the brand on the
 * left, the language and theme switches in the right corner, where the
 * shell's top bar has them.
 */
export function StandaloneTopBar() {
  return (
    <header className="ledger-masthead flex h-[var(--topbar-height)] shrink-0 items-center gap-[var(--dens-nav-gap)] px-[var(--dens-shell-x)]">
      <BrandLockup />
      <div className="ml-auto flex items-center gap-2">
        <LanguageSwitch />
        <ThemeSwitch />
      </div>
    </header>
  );
}
