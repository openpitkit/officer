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

import { useMarketData } from "@/api/useMarketData";
import { useService } from "@/api/useService";
import {
  NonReleaseNavEntry,
  RestartRequiredNavEntry,
} from "@/components/SidebarExtras";

vi.mock("@/api/useMarketData", () => ({ useMarketData: vi.fn() }));
vi.mock("@/api/useService", () => ({ useService: vi.fn() }));
vi.mock("@/components/sidebar-context", () => ({
  useSidebar: () => ({ close: vi.fn() }),
}));

const useMarketDataMock = vi.mocked(useMarketData);
const useServiceMock = vi.mocked(useService);

beforeEach(() => {
  vi.clearAllMocks();
  useMarketDataMock.mockReturnValue({
    load: {
      state: "ready",
      data: {
        providers: [],
        instances: [],
        freshnessSeconds: 30,
        restartRequired: true,
      },
      error: null,
    },
    reload: vi.fn(),
  });
  useServiceMock.mockReturnValue({
    load: {
      state: "ready",
      data: {
        name: "Pit Officer",
        engineVersion: "dev",
        engineBuildProfile: "debug",
        release: false,
        database: { path: "officer.db", reachable: true },
      },
      error: null,
    },
    reload: vi.fn(),
  });
});

describe("SidebarExtras page request ownership", () => {
  it("leaves the market-data request to its destination page", () => {
    render(
      <MemoryRouter initialEntries={["/market-data"]}>
        <RestartRequiredNavEntry />
      </MemoryRouter>,
    );

    expect(useMarketDataMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("polls market data and shows the warning away from its page", () => {
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <RestartRequiredNavEntry />
      </MemoryRouter>,
    );

    expect(useMarketDataMock).toHaveBeenCalledOnce();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/market-data");
  });

  it("leaves the service request to its destination page", () => {
    render(
      <MemoryRouter initialEntries={["/service"]}>
        <NonReleaseNavEntry />
      </MemoryRouter>,
    );

    expect(useServiceMock).not.toHaveBeenCalled();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
  });

  it("polls service state and shows the warning away from its page", () => {
    render(
      <MemoryRouter initialEntries={["/accounts"]}>
        <NonReleaseNavEntry />
      </MemoryRouter>,
    );

    expect(useServiceMock).toHaveBeenCalledOnce();
    expect(screen.getByRole("link")).toHaveAttribute("href", "/service");
  });
});
