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

// Independent state for the mobile drawer and the icon-only sidebar view.

import { useCallback, useRef, useState, type ReactNode } from "react";

import { SidebarContext } from "@/components/sidebar-context";

export function SidebarProvider({ children }: { children: ReactNode }) {
  const [open, setOpen] = useState(false);
  const [collapsed, setCollapsed] = useState(false);
  const [indicator, setIndicator] = useState<ReactNode>(null);
  const menuButtonRef = useRef<HTMLButtonElement>(null);
  const toggle = useCallback(() => setOpen((v) => !v), []);
  const close = useCallback(() => setOpen(false), []);
  const toggleCollapsed = useCallback(
    () => setCollapsed((value) => !value),
    [],
  );
  return (
    <SidebarContext.Provider
      value={{
        open,
        toggle,
        close,
        collapsed,
        toggleCollapsed,
        indicator,
        setIndicator,
        menuButtonRef,
      }}
    >
      {children}
    </SidebarContext.Provider>
  );
}
