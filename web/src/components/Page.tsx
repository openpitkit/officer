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

import { PendingRestartBanner } from "@/components/PendingRestartBanner";
import { TopBar } from "@/components/TopBar";

export function Page({
  title,
  actions,
  children,
}: {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <div className="flex h-full min-h-0 flex-col">
      <TopBar title={title} actions={actions} />
      <main className="min-h-0 flex-1 overflow-y-auto bg-bg px-[var(--dens-page-pad)] py-[calc(var(--dens-page-pad)*0.75)]">
        <div className="mx-auto w-full max-w-[var(--content-max)] space-y-[var(--dens-row-gap)]">
          <PendingRestartBanner />
          {children}
        </div>
      </main>
    </div>
  );
}
