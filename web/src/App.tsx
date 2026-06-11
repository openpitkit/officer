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
//   /positions     → Positions
//   /trading       → Trading
//   /market-data   → Market Data
//   /audit         → Audit
//   /service       → Service
//
// Per-account filtering uses query params (?account=, ?source=) rather than
// sub-routes, so feature phases must pass e.g. ?account=<id> when linking from
// an account detail context to /policies, /audit, or /trading. The pages read
// the param with useSearchParams().

import { Navigate, Route, Routes } from "react-router-dom";

import { Sidebar } from "@/components/Sidebar";
import { Accounts } from "@/pages/Accounts";
import { Audit } from "@/pages/Audit";
import { Dashboard } from "@/pages/Dashboard";
import { Limits } from "@/pages/Limits";
import { MarketData } from "@/pages/MarketData";
import { Positions } from "@/pages/Positions";
import { Service } from "@/pages/Service";
import { Trading } from "@/pages/Trading";

export default function App() {
  return (
    <div className="flex h-screen w-screen overflow-hidden bg-bg text-text">
      <Sidebar />
      <div className="flex min-w-0 flex-1 flex-col">
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/accounts" element={<Accounts />} />
          {/* /policies is the new name; /limits redirects for any bookmarks. */}
          <Route path="/policies" element={<Limits />} />
          <Route path="/limits" element={<Navigate to="/policies" replace />} />
          <Route path="/positions" element={<Positions />} />
          <Route path="/trading" element={<Trading />} />
          <Route path="/market-data" element={<MarketData />} />
          <Route path="/audit" element={<Audit />} />
          <Route path="/service" element={<Service />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </div>
    </div>
  );
}
