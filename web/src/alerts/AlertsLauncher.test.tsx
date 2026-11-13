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

import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { SidebarProvider } from "@/components/SidebarContext";
import { Button } from "@/components/ui/button";
import { ApiClientProvider } from "@/framework/api/ApiClientProvider";
import i18n from "@/i18n";

import { AlertsLauncher } from "./AlertsLauncher";
import { parseAlertList } from "./api";
import { registerAlertRenderer, type ActionableAlert } from "./registry";

registerAlertRenderer("test-ui", {
  summary: (alert) => String(alert.payload),
  Detail: ({ pending, resolve }) => (
    <>
      <Button disabled={pending} onClick={() => void resolve("fail", {})}>
        Fail decision
      </Button>
      <Button disabled={pending} onClick={() => void resolve("accept", {})}>
        Accept decision
      </Button>
    </>
  ),
});

const initial: ActionableAlert = {
  id: "event",
  type: "test-ui",
  at: "2026-11-12T08:00:00Z",
  source: "system",
  payload: "<script>unsafe()</script>",
  read: false,
  actions: ["fail", "accept"],
  deliveries: [{ channel: "other", failed: true }],
};

function mount(fetcher: typeof fetch) {
  render(
    <ApiClientProvider
      config={{
        baseUrl: "/app/api/v1",
        fetch: fetcher,
        translate: (key, params) => i18n.t(key, params),
      }}
    >
      <SidebarProvider>
        <AlertsLauncher />
      </SidebarProvider>
    </ApiClientProvider>,
  );
}

describe("actionable alerts", () => {
  it("expands without resolving, shares read mutations, retains a failed action, then resolves", async () => {
    let alert: ActionableAlert | null = { ...initial };
    const calls: string[] = [];
    const fetcher = vi.fn<typeof fetch>(async (url, options) => {
      const path = String(url);
      calls.push(`${options?.method ?? "GET"} ${path}`);
      if (path.endsWith("/read")) {
        if (alert)
          alert = { ...alert, read: JSON.parse(String(options?.body)).read };
        return Response.json({ read: alert?.read });
      }
      if (path.endsWith("/actions/fail"))
        return Response.json(
          { error: "internal", message: "internal" },
          { status: 500 },
        );
      if (path.endsWith("/actions/accept")) {
        alert = null;
        return Response.json({
          action: "accept",
          actor: "operator",
          result: {},
        });
      }
      return Response.json({
        alerts: alert ? [alert] : [],
        total: alert ? 1 : 0,
        unread: alert && !alert.read ? 1 : 0,
      });
    });
    mount(fetcher);
    const launcher = await screen.findByRole("button", {
      name: /Alerts.*Unresolved: 1/,
    });
    await userEvent.click(launcher);
    expect(calls.filter((x) => x.includes("/read"))).toHaveLength(0);
    const drawer = screen.getByRole("dialog");
    await userEvent.click(
      within(drawer).getByRole("button", { name: /<script>unsafe/ }),
    );
    await within(drawer).findByRole("button", { name: "Mark unread" });
    expect(document.querySelector("script")).toBeNull();
    expect(
      within(drawer).getByText(
        "Delivery failed: other. The alert remains available here.",
      ),
    ).toBeInTheDocument();
    expect(alert).not.toBeNull();
    await userEvent.click(
      within(drawer).getByRole("button", { name: "Mark unread" }),
    );
    await waitFor(() => expect(alert?.read).toBe(false));
    await within(drawer).findByRole("button", { name: "Mark read" });
    expect(
      within(drawer).getByRole("button", { name: /<script>unsafe/ }),
    ).toHaveAttribute("aria-expanded", "true");
    await userEvent.click(
      within(drawer).getByRole("button", { name: "Fail decision" }),
    );
    await within(drawer).findByRole("alert");
    expect(alert).not.toBeNull();
    expect(alert?.read).toBe(false);
    expect(
      calls.filter((x) => x.startsWith("PUT ") && x.endsWith("/read")),
    ).toHaveLength(2);
    await userEvent.click(
      within(drawer).getByRole("button", { name: "Accept decision" }),
    );
    await within(drawer).findByText("No unresolved alerts");
    expect(
      calls.some((x) =>
        x.includes("POST /app/api/v1/alerts/event/actions/accept"),
      ),
    ).toBe(true);
    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(launcher).toHaveFocus());
    expect(
      screen.getByRole("button", { name: /Alerts.*Unresolved: 0/ }),
    ).toBeInTheDocument();
  });

  it("shows fetch errors and retries without fabricating zero counts", async () => {
    let failing = true;
    mount(
      vi.fn<typeof fetch>(async () =>
        failing
          ? Response.json({ error: "internal" }, { status: 500 })
          : Response.json({ alerts: [], total: 0, unread: 0 }),
      ),
    );
    const launcher = await screen.findByRole("button", {
      name: /Alerts.*Could not load alerts/,
    });
    await userEvent.click(launcher);
    const retry = await screen.findByRole("button", { name: "Retry" });
    failing = false;
    await userEvent.click(retry);
    await screen.findByText("No unresolved alerts");
  });

  it("refuses unsupported response shapes instead of silently dropping events", () => {
    expect(() => parseAlertList({ alerts: [], unread: 0, total: 1 })).toThrow(
      "Inconsistent",
    );
    expect(() =>
      parseAlertList({
        alerts: [{ ...initial, at: "unknown" }],
        unread: 1,
        total: 1,
      }),
    ).toThrow("Invalid alert");
    expect(() =>
      parseAlertList({ alerts: [], unread: -1, total: 0 }),
    ).toThrow();
  });
});
