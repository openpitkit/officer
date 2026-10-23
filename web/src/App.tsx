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

import { useState } from "react";
import { Navigate, Route, Routes } from "react-router-dom";

import { SidebarProvider } from "@/components/SidebarContext";
import { StartupGate } from "@/components/StartupGate";
import { AppRoutes, Sidebar } from "@/framework";
import { panelSession } from "@/lib/panelSession";
import { Setup, SetupReview } from "@/pages/Setup";

export default function App() {
  return (
    <StartupGate>
      <Routes>
        <Route path="/setup" element={<SetupRoute />} />
        <Route path="*" element={<ShellRoute />} />
      </Routes>
    </StartupGate>
  );
}

/** The first-run wizard while setup is not complete, else its review. */
function SetupRoute() {
  // Decided once, on mount: "Finish setup" admits `token` or `open`, and the
  // wizard goes on to its last steps on this page, which a later render must
  // not turn into the review; a browser that mounts the routes again after it,
  // as on the startup gate's Retry, lands in the review.
  const [mode] = useState(() => panelSession().accessMode);
  return mode === "setup" ? <Setup /> : <SetupReview accessMode={mode} />;
}

/** The panel shell, or the first-run wizard while setup is not complete. */
function ShellRoute() {
  if (panelSession().accessMode === "setup") {
    return <Navigate to="/setup" replace />;
  }
  return (
    <SidebarProvider>
      <div className="flex h-dvh w-full overflow-hidden bg-bg text-text">
        <Sidebar />
        <div className="flex min-h-0 min-w-0 flex-1 flex-col">
          <AppRoutes />
        </div>
      </div>
    </SidebarProvider>
  );
}
