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

import { useEffect, useState } from "react";

import { SidebarProvider } from "@/components/SidebarContext";
import { WelcomeDialog } from "@/components/WelcomeDialog";
import { AppRoutes, Sidebar, useOfficerApi } from "@/framework";

export default function App() {
  const { fetchWelcomeSeen } = useOfficerApi();
  // The welcome dialog shows on every load until the operator dismisses it with
  // "don't show again", which the backend persists in user settings.
  const [welcomeOpen, setWelcomeOpen] = useState(false);

  useEffect(() => {
    let cancelled = false;
    fetchWelcomeSeen()
      .then((seen) => {
        if (!cancelled && !seen) {
          setWelcomeOpen(true);
        }
      })
      .catch(() => {
        // On a transient failure, leave the dialog closed rather than nag.
      });
    return () => {
      cancelled = true;
    };
  }, [fetchWelcomeSeen]);

  return (
    <SidebarProvider>
      <div className="flex h-screen w-screen overflow-hidden bg-bg text-text">
        <Sidebar />
        <div className="flex min-w-0 flex-1 flex-col">
          <AppRoutes home="/" />
        </div>
      </div>
      <WelcomeDialog open={welcomeOpen} onOpenChange={setWelcomeOpen} />
    </SidebarProvider>
  );
}
