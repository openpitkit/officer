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

import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { useOverview } from "@/api/useOverview";
import { useMcpAccess } from "@/api/useMcpAccess";
import { Dashboard } from "@/pages/Dashboard";
import { McpAccessCard } from "@/pages/dashboard/widgets";

vi.mock("@/api/useOverview", () => ({ useOverview: vi.fn() }));
vi.mock("@/api/useMcpAccess", () => ({ useMcpAccess: vi.fn() }));
vi.mock("@/components/Page", () => ({
  Page: ({ children }: { children: React.ReactNode }) => (
    <main>{children}</main>
  ),
}));
vi.mock("@/components/PageStates", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/components/PageStates")>()),
  ErrorState: () => <div data-testid="error-state" />,
  TableSkeleton: () => <div data-testid="table-skeleton" />,
}));
vi.mock("@/framework", () => ({
  DashboardWidgets: ({ ids }: { ids: string[] }) => (
    <div>
      {ids.includes("mcp-access-card") && <McpAccessCard />}
      {ids.includes("audit-strip") && <span>audit-strip</span>}
    </div>
  ),
}));

const useOverviewMock = vi.mocked(useOverview);
const useMcpAccessMock = vi.mocked(useMcpAccess);

function renderDashboard() {
  return render(
    <MemoryRouter>
      <Dashboard />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  useMcpAccessMock.mockReturnValue({
    load: {
      state: "ready",
      data: [
        {
          name: "list_orders",
          title: "List orders",
          agentDescription: "List orders",
          mutating: false,
          protective: false,
          implemented: true,
          enabled: true,
        },
      ],
      error: null,
    },
    reload: vi.fn(),
  });
});

describe("Dashboard first load", () => {
  it("keeps the independent audit widget while the overview is loading", () => {
    useOverviewMock.mockReturnValue({
      load: { state: "loading", data: null, error: null },
      reload: vi.fn(),
    });

    renderDashboard();

    expect(screen.getByTestId("table-skeleton")).toBeInTheDocument();
    expect(screen.getByText("audit-strip")).toBeInTheDocument();
  });

  it("keeps the independent audit widget when the overview fails", () => {
    useOverviewMock.mockReturnValue({
      load: { state: "error", data: null, error: "offline" },
      reload: vi.fn(),
    });

    renderDashboard();

    expect(screen.getByTestId("error-state")).toBeInTheDocument();
    expect(screen.getByText("audit-strip")).toBeInTheDocument();
  });

  it("renders the audit widget with the ready overview", () => {
    useOverviewMock.mockReturnValue({
      load: {
        state: "ready",
        data: {
          counts: {
            accounts: 0,
            accountsActive: 0,
            groups: 0,
            groupsActive: 0,
            limits: 0,
            ordersActive: 0,
            ordersToday: 0,
            ordersTotal: 0,
          },
          activity: [],
        },
        error: null,
      },
      reload: vi.fn(),
    });

    renderDashboard();

    expect(screen.getByText("audit-strip")).toBeInTheDocument();
  });

  it("marks nested MCP data stale while the overview stays healthy", () => {
    useOverviewMock.mockReturnValue({
      load: {
        state: "ready",
        data: {
          counts: {
            accounts: 0,
            accountsActive: 0,
            groups: 0,
            groupsActive: 0,
            limits: 0,
            ordersActive: 0,
            ordersToday: 0,
            ordersTotal: 0,
          },
          activity: [],
        },
        error: null,
      },
      reload: vi.fn(),
    });
    useMcpAccessMock.mockReturnValue({
      load: {
        state: "ready",
        data: [
          {
            name: "list_orders",
            title: "List orders",
            agentDescription: "List orders",
            mutating: false,
            protective: false,
            implemented: true,
            enabled: true,
          },
        ],
        error: "MCP refresh failed",
      },
      reload: vi.fn(),
    });

    renderDashboard();

    expect(screen.getByText("list_orders")).toBeInTheDocument();
    expect(screen.getByText("Stale data")).toBeInTheDocument();
  });
});
