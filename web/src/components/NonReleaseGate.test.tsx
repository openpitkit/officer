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

import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";

import type { BuildStatus } from "@/api/build";
import { NonReleaseGate } from "@/components/NonReleaseGate";
import { SidebarProvider } from "@/components/SidebarContext";
import { NonReleaseNavEntry } from "@/components/SidebarExtras";
import {
  errorResponse,
  jsonResponse,
  renderWithApi,
  serving,
} from "@/test/apiClient";
import { ThemeProvider } from "@/theme/ThemeProvider";

const CHOICE: BuildStatus = {
  release: false,
  phase: "choice",
  releaseDatabaseExists: true,
};
const CHILD = "The panel child";
const COPY = "Copy the release database";
const EMPTY = "Start with an empty database";
const FAILURE = "Could not load data";
const BANNER = "Data from this build will never be migrated.";

function renderGate(fetchMock: typeof fetch) {
  return renderWithApi(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <NonReleaseGate>
        <p>{CHILD}</p>
      </NonReleaseGate>
    </ThemeProvider>,
    { fetch: fetchMock },
  );
}

function routes(
  status: BuildStatus,
  post = () => jsonResponse({ ...CHOICE, phase: "preparing" }, 202),
) {
  return serving({
    "GET /app/api/v1/build": () => jsonResponse(status),
    "POST /app/api/v1/build/database": post,
  });
}

function posts(fetchMock: ReturnType<typeof serving>) {
  return fetchMock.mock.calls.filter(([, init]) => init.method === "POST");
}

async function openConfirmation() {
  await userEvent.click(
    await screen.findByRole("button", { name: "Continue" }),
  );
  return screen.getByRole("alertdialog");
}

afterEach(() => vi.useRealTimers());

describe("NonReleaseGate", () => {
  it("hides the panel while the initial status is pending", () => {
    renderGate(vi.fn(() => new Promise<Response>(() => {})));
    expect(screen.getByRole("status", { name: "Loading" })).toHaveAttribute(
      "aria-busy",
      "true",
    );
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it("admits a ready release without a banner or polling", async () => {
    vi.useFakeTimers();
    const fetchMock = routes({ ...CHOICE, release: true, phase: "ready" });
    await act(async () => {
      renderGate(fetchMock as unknown as typeof fetch);
    });
    expect(screen.getByText(CHILD)).toBeInTheDocument();
    expect(screen.queryByText("non-release")).not.toBeInTheDocument();
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("shows the banner above ready non-release children and stops polling", async () => {
    vi.useFakeTimers();
    const fetchMock = routes({ ...CHOICE, phase: "ready" });
    await act(async () => {
      renderGate(fetchMock as unknown as typeof fetch);
    });
    expect(screen.getByText(CHILD)).toBeInTheDocument();
    expect(screen.getByText("non-release")).toHaveClass("uppercase");
    expect(screen.getByText(BANNER)).toBeVisible();
    await act(() => vi.advanceTimersByTimeAsync(5000));
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each(["/accounts", "/service"])(
    "moves the banner into the sidebar footer on %s",
    async (path) => {
      await act(async () => {
        renderWithApi(
          <MemoryRouter initialEntries={[path]}>
            <NonReleaseGate>
              <SidebarProvider>
                <NonReleaseNavEntry />
              </SidebarProvider>
            </NonReleaseGate>
          </MemoryRouter>,
          {
            fetch: routes({
              ...CHOICE,
              phase: "ready",
            }) as unknown as typeof fetch,
          },
        );
      });
      const banners = screen.getAllByText(BANNER);
      expect(banners).toHaveLength(2);
      const link = screen.getByRole("link");
      expect(link).toHaveAttribute("href", "/service");
      expect(link).toHaveTextContent(BANNER);
      // The top strip stays only below md, where the sidebar is a drawer.
      const strip = banners.find((banner) => !link.contains(banner));
      expect(strip?.closest(".md\\:hidden")).not.toBeNull();
    },
  );

  it("keeps the top strip whole when the sidebar entry leaves", async () => {
    function Shell() {
      const [shown, setShown] = useState(true);
      return (
        <SidebarProvider>
          {shown && <NonReleaseNavEntry />}
          <button type="button" onClick={() => setShown(false)}>
            Leave the shell
          </button>
        </SidebarProvider>
      );
    }
    await act(async () => {
      renderWithApi(
        <MemoryRouter>
          <NonReleaseGate>
            <Shell />
          </NonReleaseGate>
        </MemoryRouter>,
        {
          fetch: routes({
            ...CHOICE,
            phase: "ready",
          }) as unknown as typeof fetch,
        },
      );
    });
    await userEvent.click(
      screen.getByRole("button", { name: "Leave the shell" }),
    );
    expect(screen.getAllByText(BANNER)).toHaveLength(1);
    expect(screen.getByText(BANNER).closest(".md\\:hidden")).toBeNull();
  });

  it("offers both options, explains stopping the release and confirms a copy", async () => {
    const fetchMock = routes(CHOICE);
    renderGate(fetchMock as unknown as typeof fetch);
    const copy = await screen.findByRole("radio", { name: COPY });
    expect(screen.getAllByText("non-release")).toHaveLength(1);
    expect(screen.getAllByText(BANNER)).toHaveLength(1);
    expect(
      screen.getByText("This build is for quick tests only"),
    ).toBeVisible();
    expect(
      screen.getByText("This build never changes the release database."),
    ).toBeVisible();
    expect(screen.getByRole("radio", { name: EMPTY })).toBeInTheDocument();
    expect(copy).toHaveAccessibleDescription(
      /Stop the release build before copying\./,
    );
    await userEvent.click(copy);
    const dialog = await openConfirmation();
    expect(dialog).toHaveTextContent("Stop the release build first.");
    expect(posts(fetchMock)).toHaveLength(0);
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Prepare database" }),
    );
    expect(
      await screen.findByText("Preparing this build's database…"),
    ).toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(1);
    expect(JSON.parse(posts(fetchMock)[0][1].body as string)).toEqual({
      fromRelease: true,
    });
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it("disables the copy and explains why when no release database exists", async () => {
    const fetchMock = routes({ ...CHOICE, releaseDatabaseExists: false });
    renderGate(fetchMock as unknown as typeof fetch);
    expect(await screen.findByRole("radio", { name: EMPTY })).toBeChecked();
    const copy = screen.getByRole("radio", { name: COPY });
    expect(copy).toBeDisabled();
    expect(copy).toHaveAccessibleDescription(/Stable database not found$/);
    expect(screen.getAllByRole("radio")).toHaveLength(2);
    await userEvent.click(copy);
    expect(screen.getByRole("radio", { name: EMPTY })).toBeChecked();
    const dialog = await openConfirmation();
    expect(dialog).toHaveTextContent("An empty database will be created");
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Prepare database" }),
    );
    await screen.findByText("Preparing this build's database…");
    expect(posts(fetchMock)).toHaveLength(1);
    expect(JSON.parse(posts(fetchMock)[0][1].body as string)).toEqual({
      fromRelease: false,
    });
  });

  it("sends nothing when the dialog is cancelled", async () => {
    const fetchMock = routes(CHOICE);
    renderGate(fetchMock as unknown as typeof fetch);
    const dialog = await openConfirmation();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Cancel" }),
    );
    expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(0);
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it("disables all controls and keeps at most one choice request in flight", async () => {
    let finish!: (response: Response) => void;
    const fetchMock = routes(CHOICE);
    fetchMock.mockImplementation(async (_url, init) =>
      init.method === "POST"
        ? new Promise<Response>((resolve) => {
            finish = resolve;
          })
        : jsonResponse(CHOICE),
    );
    renderGate(fetchMock as unknown as typeof fetch);
    const dialog = await openConfirmation();
    const confirm = within(dialog).getByRole("button", {
      name: "Prepare database",
    });
    await userEvent.dblClick(confirm);
    expect(confirm).toBeDisabled();
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Continue", hidden: true }),
    ).toBeDisabled();
    for (const radio of screen.getAllByRole("radio", { hidden: true }))
      expect(radio).toBeDisabled();
    expect(posts(fetchMock)).toHaveLength(1);
    await act(async () =>
      finish(jsonResponse({ ...CHOICE, phase: "ready" }, 202)),
    );
    expect(screen.getByText(CHILD)).toBeInTheDocument();
  });

  it.each(["ready", "failed"] as const)(
    "polls preparing until %s, then stops",
    async (phase) => {
      vi.useFakeTimers();
      const final =
        phase === "failed"
          ? { ...CHOICE, phase, error: "migration failed" }
          : { ...CHOICE, phase };
      const fetchMock = vi
        .fn()
        .mockImplementationOnce(async () =>
          jsonResponse({ ...CHOICE, phase: "preparing" }),
        )
        .mockImplementationOnce(async () =>
          jsonResponse({ ...CHOICE, phase: "preparing" }),
        )
        .mockImplementation(async () => jsonResponse(final));
      await act(async () => {
        renderGate(fetchMock);
      });
      expect(
        screen.getByRole("status", {
          name: "Preparing this build's database…",
        }),
      ).toBeInTheDocument();
      expect(screen.getByText("non-release")).toBeVisible();
      expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
      await act(() => vi.advanceTimersByTimeAsync(999));
      expect(fetchMock).toHaveBeenCalledTimes(1);
      await act(() => vi.advanceTimersByTimeAsync(1));
      await act(() => vi.advanceTimersByTimeAsync(1000));
      expect(fetchMock).toHaveBeenCalledTimes(3);
      if (phase === "ready")
        expect(screen.getByText(CHILD)).toBeInTheDocument();
      else {
        expect(screen.getByRole("alert")).toHaveTextContent("migration failed");
        expect(screen.getByRole("radio", { name: COPY })).toBeInTheDocument();
      }
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(fetchMock).toHaveBeenCalledTimes(3);
    },
  );

  it("shows C5's failure and offers the choice again", async () => {
    renderGate(
      routes({
        ...CHOICE,
        phase: "failed",
        error: "migration failed",
      }) as unknown as typeof fetch,
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "migration failed",
    );
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not prepare this build",
    );
    expect(screen.getByRole("radio", { name: COPY })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: EMPTY })).toBeInTheDocument();
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it.each(["network", "decode"])(
    "blocks the panel on a failed %s status load and retries",
    async (failure) => {
      const fetchMock = vi
        .fn()
        .mockImplementationOnce(async () => {
          if (failure === "network") throw new TypeError("connection refused");
          return jsonResponse({ ...CHOICE, phase: "unknown" });
        })
        .mockImplementation(async () =>
          jsonResponse({ ...CHOICE, release: true, phase: "ready" }),
        );
      renderGate(fetchMock);
      expect(await screen.findByText(FAILURE)).toBeInTheDocument();
      expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
      expect(
        screen.getByText(
          failure === "network"
            ? /connection refused/
            : /build response: phase must/,
        ),
      ).toBeInTheDocument();
      await userEvent.click(screen.getByRole("button", { name: "Retry" }));
      expect(await screen.findByText(CHILD)).toBeInTheDocument();
      expect(fetchMock).toHaveBeenCalledTimes(2);
    },
  );

  it("keeps the banner on a failed poll and while retry is loading", async () => {
    vi.useFakeTimers();
    const fetchMock = vi
      .fn()
      .mockImplementationOnce(async () =>
        jsonResponse({ ...CHOICE, phase: "preparing" }),
      )
      .mockRejectedValueOnce(new TypeError("connection refused"))
      .mockImplementation(() => new Promise<Response>(() => {}));
    await act(async () => {
      renderGate(fetchMock);
    });
    await act(() => vi.advanceTimersByTimeAsync(1000));
    expect(screen.getByText(FAILURE)).toBeInTheDocument();
    expect(screen.getByText("non-release")).toBeVisible();
    await act(async () =>
      screen.getByRole("button", { name: "Retry" }).click(),
    );
    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
    expect(screen.getByText("non-release")).toBeVisible();
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it("shows a 409 on the first screen without retrying the choice", async () => {
    const fetchMock = routes(CHOICE, () =>
      errorResponse(409, "conflict", {}, "database choice already in progress"),
    );
    renderGate(fetchMock as unknown as typeof fetch);
    const dialog = await openConfirmation();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Prepare database" }),
    );
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "database choice already in progress",
    );
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(screen.getByRole("radio", { name: COPY })).toBeEnabled();
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
    expect(posts(fetchMock)).toHaveLength(1);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("refreshes the status after a 409 only when the operator asks", async () => {
    const fetchMock = routes(CHOICE, () =>
      errorResponse(409, "conflict", {}, "database choice already in progress"),
    );
    renderGate(fetchMock as unknown as typeof fetch);
    const dialog = await openConfirmation();
    await userEvent.click(
      within(dialog).getByRole("button", { name: "Prepare database" }),
    );
    const notice = await screen.findByRole("alert");
    expect(notice).toHaveTextContent("Could not prepare this build");
    expect(notice).toHaveTextContent("database choice already in progress");
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );
    expect(fetchMock).toHaveBeenCalledTimes(2);
    let finish!: (response: Response) => void;
    fetchMock.mockImplementationOnce(
      () =>
        new Promise<Response>((resolve) => {
          finish = resolve;
        }),
    );
    await userEvent.click(
      within(notice).getByRole("button", { name: "Refresh" }),
    );
    expect(screen.getByRole("status", { name: "Loading" })).toBeInTheDocument();
    expect(screen.getByText("non-release")).toBeVisible();
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(fetchMock.mock.calls[2][0]).toBe("/app/api/v1/build");
    expect(fetchMock.mock.calls[2][1].method).toBe("GET");
    await act(async () =>
      finish(jsonResponse({ ...CHOICE, phase: "preparing" })),
    );
    expect(
      screen.getByRole("status", { name: "Preparing this build's database…" }),
    ).toBeVisible();
    expect(posts(fetchMock)).toHaveLength(1);
    expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
  });

  it.each(["choice", "preparing", "failed"] as const)(
    "rejects a release in phase %s",
    async (phase) => {
      const status: BuildStatus =
        phase === "failed"
          ? { ...CHOICE, release: true, phase, error: "failed" }
          : { ...CHOICE, release: true, phase };
      renderGate(routes(status) as unknown as typeof fetch);
      expect(await screen.findByText(FAILURE)).toBeInTheDocument();
      expect(
        screen.getByText(/must be ready for a release build/),
      ).toBeInTheDocument();
      expect(screen.queryByText(CHILD)).not.toBeInTheDocument();
      expect(screen.queryByText("non-release")).not.toBeInTheDocument();
    },
  );
});
