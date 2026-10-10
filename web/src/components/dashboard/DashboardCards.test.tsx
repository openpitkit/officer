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

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";

import type { LoadState } from "@/api/usePolling";
import {
  DashboardItemChips,
  DashboardNote,
  DashboardSourceError,
  DashboardSourceRow,
  DashboardStatusCard,
} from "@/components/dashboard";

function renderCard(load: LoadState<string>) {
  return render(
    <MemoryRouter>
      <Routes>
        <Route
          path="/"
          element={
            <DashboardStatusCard title="Feeds" to="/feeds" load={load}>
              {(data) => <span>data: {data}</span>}
            </DashboardStatusCard>
          }
        />
        <Route path="/feeds" element={<p>feeds page</p>} />
      </Routes>
    </MemoryRouter>,
  );
}

describe("DashboardStatusCard", () => {
  it("renders loaded data after the title and opens its route", async () => {
    renderCard({ state: "ready", data: "two feeds", error: null });

    expect(screen.getByText("Feeds")).toBeInTheDocument();
    expect(screen.getByText("data: two feeds")).toBeInTheDocument();
    expect(screen.queryByText("Stale data")).not.toBeInTheDocument();

    await userEvent.click(screen.getByRole("button"));

    expect(screen.getByText("feeds page")).toBeInTheDocument();
  });

  it("keeps retained data and marks it stale after a failed refresh", () => {
    renderCard({ state: "ready", data: "two feeds", error: "offline" });

    expect(screen.getByText("data: two feeds")).toBeInTheDocument();
    expect(screen.getByText("Stale data")).toBeInTheDocument();
  });

  it("shows the load error instead of content", () => {
    renderCard({ state: "error", data: null, error: "offline" });

    expect(screen.getByText("offline")).toBeInTheDocument();
    expect(screen.queryByText(/^data:/)).not.toBeInTheDocument();
  });

  it("shows a spinner and no content while loading", () => {
    const { container } = renderCard({
      state: "loading",
      data: null,
      error: null,
    });

    expect(screen.getByText("Feeds")).toBeInTheDocument();
    expect(container.querySelector(".animate-spin")).toBeInTheDocument();
    expect(screen.queryByText(/^data:/)).not.toBeInTheDocument();
  });

  it("shows no spinner once loaded", () => {
    const { container } = renderCard({
      state: "ready",
      data: "two feeds",
      error: null,
    });

    expect(container.querySelector(".animate-spin")).not.toBeInTheDocument();
  });
});

describe("DashboardNote", () => {
  it("renders its text muted", () => {
    render(<DashboardNote>No feeds enabled</DashboardNote>);

    expect(screen.getByText("No feeds enabled")).toHaveClass("text-muted-lt");
  });
});

describe("DashboardSourceRow", () => {
  it("shows the source name before its status", () => {
    render(
      <DashboardSourceRow label="Feed one">
        <span>status chip</span>
      </DashboardSourceRow>,
    );

    const name = screen.getByText("Feed one");
    expect(name.nextSibling).toHaveTextContent("status chip");
  });
});

describe("DashboardItemChips", () => {
  const items = ["A/USD", "B/USD", "C/USD", "D/USD", "E/USD"].map((text) => ({
    key: text,
    text,
  }));

  it("shows items up to the limit and counts the rest", () => {
    render(
      <DashboardItemChips
        items={items}
        variant="ok"
        limit={3}
        moreLabel={(count) => `+${count} more`}
      />,
    );

    expect(screen.getByText("A/USD")).toHaveClass("text-[var(--ok)]");
    expect(screen.getByText("C/USD")).toBeInTheDocument();
    expect(screen.queryByText("D/USD")).not.toBeInTheDocument();
    expect(screen.getByText("+2 more")).toBeInTheDocument();
  });

  it("omits the count when every item fits", () => {
    render(
      <DashboardItemChips
        items={items.slice(0, 2)}
        variant="neutral"
        limit={3}
        moreLabel={(count) => `+${count} more`}
      />,
    );

    expect(screen.getByText("B/USD")).toHaveClass("text-muted");
    expect(screen.queryByText(/more$/)).not.toBeInTheDocument();
  });
});

describe("DashboardSourceError", () => {
  it("clips the error text after a colon", () => {
    render(
      <DashboardSourceError
        title="Error Feed one"
        error={"x".repeat(45)}
        diagnostic="Not shown"
      />,
    );

    expect(
      screen.getByText(`Error Feed one: ${"x".repeat(40)}…`),
    ).toBeInTheDocument();
  });

  it("names the latest diagnostic after a dash when there is no error text", () => {
    render(<DashboardSourceError title="Error Feed one" diagnostic="Auth" />);

    expect(screen.getByText("Error Feed one - Auth")).toBeInTheDocument();
  });

  it("clips the diagnostic title", () => {
    render(
      <DashboardSourceError
        title="Error Feed one"
        diagnostic={"d".repeat(45)}
      />,
    );

    expect(
      screen.getByText(`Error Feed one - ${"d".repeat(40)}…`),
    ).toBeInTheDocument();
  });

  it("shows the title alone without a cause", () => {
    render(<DashboardSourceError title="Error Feed one" />);

    expect(screen.getByText("Error Feed one")).toBeInTheDocument();
  });
});
