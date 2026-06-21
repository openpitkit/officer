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

// Route table:
//   /              → Dashboard
//   /accounts      → Accounts
//   /policies      → Policies (renamed Limits; 2C edits pages/Limits.tsx in place)
//   /limits        → redirect /policies
//   /positions     → Positions
//   /orders        → Orders (renamed Trading)
//   /trading       → redirect /orders
//   /market-data   → Market Data
//   /audit         → Audit
//   /mcp-access    → MCP access
//   /signing-keys  → Signing Keys
//   /service       → Service
//
// Per-account filtering uses query params (?account=, ?source=) rather than
// sub-routes, so feature phases must pass e.g. ?account=<id> when linking from
// an account detail context to /policies, /audit, or /trading. The pages read
// the param with useSearchParams().

import { useEffect, useState } from "react";
import { Navigate, Route, Routes, useLocation } from "react-router-dom";

import { fetchWelcomeSeen } from "@/api/client";
import { Sidebar } from "@/components/Sidebar";
import { SidebarProvider } from "@/components/SidebarContext";
import { WelcomeDialog } from "@/components/WelcomeDialog";
import { Accounts } from "@/pages/Accounts";
import { Audit } from "@/pages/Audit";
import { Dashboard } from "@/pages/Dashboard";
import { Limits } from "@/pages/Limits";
import { MarketData } from "@/pages/MarketData";
import { McpAccess } from "@/pages/McpAccess";
import { Positions } from "@/pages/Positions";
import { Orders } from "@/pages/Orders";
import { Service } from "@/pages/Service";
import { SigningKeys } from "@/pages/SigningKeys";

// Redirect to a canonical path while preserving the query string and hash, so
// per-account links like /trading?account=<id> keep their filter on arrival.
function RedirectPreservingQuery({ to }: { to: string }) {
  const { search, hash } = useLocation();
  return <Navigate to={{ pathname: to, search, hash }} replace />;
}

export default function App() {
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
  }, []);

  return (
    <SidebarProvider>
      <div className="flex h-screen w-screen overflow-hidden bg-bg text-text">
        <Sidebar />
        <div className="flex min-w-0 flex-1 flex-col">
          <Routes>
            <Route path="/" element={<Dashboard />} />
            <Route path="/accounts" element={<Accounts />} />
            {/* /policies is the new name; /limits redirects for any bookmarks. */}
            <Route path="/policies" element={<Limits />} />
            <Route path="/limits" element={<RedirectPreservingQuery to="/policies" />} />
            <Route path="/positions" element={<Positions />} />
            <Route path="/orders" element={<Orders />} />
            {/* /trading redirects for any bookmarks. */}
            <Route path="/trading" element={<RedirectPreservingQuery to="/orders" />} />
            <Route path="/market-data" element={<MarketData />} />
            <Route path="/audit" element={<Audit />} />
            <Route path="/mcp-access" element={<McpAccess />} />
            <Route path="/signing-keys" element={<SigningKeys />} />
            <Route path="/service" element={<Service />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </div>
      </div>
      <WelcomeDialog open={welcomeOpen} onOpenChange={setWelcomeOpen} />
    </SidebarProvider>
  );
}
