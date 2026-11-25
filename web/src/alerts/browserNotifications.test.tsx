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

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode } from "react";
import { I18nextProvider } from "react-i18next";
import { afterEach, beforeEach, expect, it, vi } from "vitest";

import type { LoadState } from "@/api/usePolling";
import i18n from "@/i18n";

import type { AlertList } from "./api";
import unreadIcon from "./favicon-unread.svg?no-inline";
import { NotificationPermissionNotice } from "./NotificationPermissionNotice";
import type { ActionableAlert } from "./registry";
import { Detail, summary } from "./TradingAccessAlert";

class BrowserNotification extends EventTarget {
  static permission: NotificationPermission;
  static requestPermission = vi.fn<() => Promise<NotificationPermission>>();
  static attempts =
    vi.fn<(title: string, options?: NotificationOptions) => void>();
  static shown: BrowserNotification[];
  close = vi.fn();

  constructor(title: string, options?: NotificationOptions) {
    super();
    BrowserNotification.attempts(title, options);
    BrowserNotification.shown.push(this);
  }
}

const initial: ActionableAlert = {
  id: "first",
  type: "mixed_trading_access",
  at: "2026-11-16T00:00:00Z",
  source: "panel",
  payload: { account: "desk", title: "Desk" },
  read: false,
  actions: ["acknowledge"],
  deliveries: [],
};
const newAlert = { ...initial, id: "second" };
const originalTitle = document.title;
let icon: HTMLLinkElement;
let useBrowserNotifications: typeof import("./browserNotifications").useBrowserNotifications;

function ready(...alerts: ActionableAlert[]) {
  return {
    state: "ready" as const,
    data: {
      alerts,
      total: alerts.length,
      unread: alerts.filter((alert) => !alert.read).length,
    },
    error: null,
  };
}

// Starts a new tab: fresh module state, with the alert renderer registered.
async function openTab() {
  vi.resetModules();
  const tab = await import("./browserNotifications");
  const { registerAlertRenderer } = await import("./registry");
  registerAlertRenderer(initial.type, { summary, Detail });
  return tab.useBrowserNotifications;
}

function Panel({
  load,
  hook = useBrowserNotifications,
}: {
  load: LoadState<AlertList>;
  hook?: typeof useBrowserNotifications;
}) {
  const permission = hook(load, i18n.t, () => {});
  return (
    <I18nextProvider i18n={i18n}>
      <NotificationPermissionNotice {...permission} />
    </I18nextProvider>
  );
}

beforeEach(async () => {
  // Each test starts a new tab with its own module state.
  useBrowserNotifications = await openTab();
  await i18n.changeLanguage("en");
  BrowserNotification.permission = "granted";
  BrowserNotification.requestPermission.mockReset();
  BrowserNotification.attempts.mockReset();
  BrowserNotification.shown = [];
  vi.stubGlobal("Notification", BrowserNotification);
  document.title = "Pit Officer";
  icon = document.createElement("link");
  icon.rel = "icon";
  icon.setAttribute("href", "/favicon.svg");
  document.head.append(icon);
});

afterEach(() => {
  cleanup();
  icon.remove();
  document.title = originalTitle;
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

it("uses the first successful load as the notification baseline", () => {
  const { rerender } = render(<Panel load={ready(initial)} />);
  expect(BrowserNotification.attempts).not.toHaveBeenCalled();

  rerender(<Panel load={ready(initial, newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledExactlyOnceWith(
    "Pit Officer: new alert",
    { body: "Paper and live access: Desk (desk)", tag: "second" },
  );

  rerender(<Panel load={ready(initial, newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledTimes(1);
});

it("remembers later read alerts without notifying", () => {
  const { rerender } = render(<Panel load={ready()} />);
  rerender(<Panel load={ready({ ...newAlert, read: true })} />);
  rerender(<Panel load={ready(newAlert)} />);
  expect(BrowserNotification.attempts).not.toHaveBeenCalled();
});

it("remembers alerts seen without permission", () => {
  BrowserNotification.permission = "default";
  const { rerender } = render(<Panel load={ready()} />);
  rerender(<Panel load={ready(newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledExactlyOnceWith(
    "",
    undefined,
  );
  expect(BrowserNotification.shown[0].close).toHaveBeenCalledTimes(1);

  BrowserNotification.permission = "granted";
  rerender(<Panel load={ready(newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledTimes(1);
});

it("ignores stale data for both notifications and the tab badge", () => {
  const { rerender } = render(<Panel load={ready()} />);
  rerender(<Panel load={{ ...ready(newAlert), error: "Unavailable" }} />);
  expect(BrowserNotification.attempts).not.toHaveBeenCalled();
  expect(document.title).toBe("Pit Officer");

  rerender(<Panel load={ready(newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledTimes(1);
});

it("shows a constructor failure and does not retry that alert", async () => {
  BrowserNotification.attempts.mockImplementation(() => {
    throw new Error("Notification constructor failed");
  });
  const { rerender } = render(<Panel load={ready()} />);
  rerender(<Panel load={ready(newAlert)} />);
  const notice = await screen.findByRole("alert");
  expect(notice).toHaveTextContent("Browser notifications failed");
  expect(notice).toHaveTextContent("Notification constructor failed");

  rerender(<Panel load={ready(newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledTimes(1);
});

it.each([
  [new Event("error"), "The browser refused to show the notification."],
  [new ErrorEvent("error", { message: "Display failed" }), "Display failed"],
] as const)("shows the browser's notification error event", (event, detail) => {
  const { rerender } = render(<Panel load={ready()} />);
  rerender(<Panel load={ready(newAlert)} />);
  act(() => {
    BrowserNotification.shown[0].dispatchEvent(event);
  });
  const notice = screen.getByRole("alert");
  expect(notice).toHaveTextContent("Browser notifications failed");
  expect(notice).toHaveTextContent(detail);
});

it("updates and restores the tab badge independently of permission", () => {
  BrowserNotification.permission = "denied";
  const { rerender, unmount } = render(
    <Panel load={ready(initial, newAlert)} />,
  );
  expect(document.title).toBe("(2) Pit Officer");
  expect(icon.getAttribute("href")).toBe(unreadIcon);

  rerender(<Panel load={ready()} />);
  expect(document.title).toBe("Pit Officer");
  expect(icon.getAttribute("href")).toBe("/favicon.svg");

  rerender(<Panel load={ready(newAlert)} />);
  rerender(<Panel load={{ ...ready(newAlert), error: "Unavailable" }} />);
  expect(document.title).toBe("Pit Officer");
  expect(icon.getAttribute("href")).toBe("/favicon.svg");

  rerender(<Panel load={ready(newAlert)} />);
  unmount();
  expect(document.title).toBe("Pit Officer");
  expect(icon.getAttribute("href")).toBe("/favicon.svg");
});

it("reports a missing favicon link", () => {
  icon.remove();
  expect(() => render(<Panel load={ready()} />)).toThrow(
    'Missing link[rel="icon"]',
  );
});

it("shows that unsupported browsers cannot send notifications", () => {
  vi.stubGlobal("Notification", undefined);
  render(<Panel load={ready()} />);
  expect(
    screen.getByText("Browser notifications are not available in this browser"),
  ).toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("shows when permission is granted", () => {
  render(<Panel load={ready()} />);
  expect(screen.getByText("Browser notifications are on")).toBeInTheDocument();
});

it("reflects permission changes made outside the drawer on the next render", () => {
  BrowserNotification.permission = "default";
  const { rerender } = render(<Panel load={ready()} />);
  expect(screen.getByText("Browser notifications are off")).toBeInTheDocument();

  BrowserNotification.permission = "granted";
  rerender(<Panel load={ready()} />);
  expect(screen.getByText("Browser notifications are on")).toBeInTheDocument();

  BrowserNotification.permission = "denied";
  rerender(<Panel load={ready()} />);
  expect(
    screen.getByText("Browser notifications are blocked"),
  ).toBeInTheDocument();
});

it("requests permission only on click and shows the current permission afterward", async () => {
  BrowserNotification.permission = "default";
  BrowserNotification.requestPermission.mockImplementation(async () => {
    BrowserNotification.permission = "denied";
    return "denied";
  });
  render(<Panel load={ready()} />);
  expect(screen.getByText("Browser notifications are off")).toBeInTheDocument();
  expect(BrowserNotification.requestPermission).not.toHaveBeenCalled();

  await userEvent.click(
    screen.getByRole("button", { name: "Allow notifications" }),
  );
  expect(BrowserNotification.requestPermission).toHaveBeenCalledTimes(1);
  expect(
    await screen.findByText("Browser notifications are blocked"),
  ).toBeInTheDocument();
  expect(screen.getByText(/site settings/)).toBeInTheDocument();
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});

it("shows a rejected permission request and lets the operator retry", async () => {
  BrowserNotification.permission = "default";
  BrowserNotification.requestPermission
    .mockRejectedValueOnce(new Error("Prompt failed"))
    .mockImplementationOnce(async () => {
      BrowserNotification.permission = "granted";
      return "granted";
    });
  render(<Panel load={ready()} />);

  await userEvent.click(
    screen.getByRole("button", { name: "Allow notifications" }),
  );
  const notice = await screen.findByRole("alert");
  expect(notice).toHaveTextContent("Browser notifications failed");
  expect(notice).toHaveTextContent("Prompt failed");
  expect(screen.getByText("Browser notifications are off")).toBeVisible();
  const allow = screen.getByRole("button", { name: "Allow notifications" });
  expect(allow).toBeVisible();

  await userEvent.click(allow);
  expect(BrowserNotification.requestPermission).toHaveBeenCalledTimes(2);
  expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  expect(await screen.findByText("Browser notifications are on")).toBeVisible();
});

const notifiedAlert: ActionableAlert = {
  ...newAlert,
  payload: { account: "desk", title: "New alert" },
};

async function mountLauncher() {
  const [{ AlertsLauncher }, { SidebarProvider }, { ApiClientProvider }] =
    await Promise.all([
      import("./AlertsLauncher"),
      import("@/components/SidebarContext"),
      import("@/framework/api/ApiClientProvider"),
    ]);
  const focus = vi.spyOn(window, "focus").mockImplementation(() => {});
  vi.useFakeTimers();

  const state = { alerts: [] as ActionableAlert[], loadError: false };
  const markRead = vi.fn();
  const acknowledge = vi.fn<() => Promise<void>>();
  const fetcher = vi.fn<typeof fetch>(async (url, options) => {
    const path = String(url);
    if (path.endsWith("/read")) {
      markRead(path, options?.method, options?.body);
      state.alerts = state.alerts.map((alert) =>
        path.endsWith(`/alerts/${alert.id}/read`)
          ? { ...alert, read: true }
          : alert,
      );
      return Response.json({ read: true });
    }
    if (path.endsWith(`/alerts/${initial.id}/actions/acknowledge`)) {
      await acknowledge();
      state.alerts = state.alerts.filter((alert) => alert.id !== initial.id);
      return Response.json({
        action: "acknowledge",
        actor: "operator",
        result: {},
      });
    }
    if (state.loadError) {
      return Response.json({ error: "internal" }, { status: 500 });
    }
    return Response.json(ready(...state.alerts).data);
  });

  await act(async () => {
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
  });
  const launcher = screen.getByRole("button", {
    name: /Alerts.*Unresolved: 0/,
  });
  return { acknowledge, focus, launcher, markRead, state };
}

function pollAlerts() {
  return act(() => vi.advanceTimersByTimeAsync(5_000));
}

it("notifies without the Web Locks API", async () => {
  expect(navigator.locks).toBeUndefined();
  const { launcher, state } = await mountLauncher();

  state.alerts = [newAlert];
  await pollAlerts();
  await act(async () => {
    fireEvent.click(launcher);
  });

  expect(BrowserNotification.attempts).toHaveBeenCalledExactlyOnceWith(
    "Pit Officer: new alert",
    { body: "Paper and live access: Desk (desk)", tag: newAlert.id },
  );
  expect(
    within(screen.getByRole("dialog")).queryByText(
      "Browser notifications are not available in this browser",
    ),
  ).not.toBeInTheDocument();
});

it("each tab notifies the same new alert on its own", async () => {
  const firstHook = await openTab();
  const secondHook = await openTab();
  expect(secondHook).not.toBe(firstHook);

  const first = render(<Panel hook={firstHook} load={ready(initial)} />);
  const other = render(<Panel hook={secondHook} load={ready(initial)} />);
  expect(BrowserNotification.attempts).not.toHaveBeenCalled();

  first.rerender(<Panel hook={firstHook} load={ready(initial, newAlert)} />);
  other.rerender(<Panel hook={secondHook} load={ready(initial, newAlert)} />);
  expect(BrowserNotification.attempts).toHaveBeenCalledTimes(2);
  for (const call of [1, 2]) {
    expect(BrowserNotification.attempts).toHaveBeenNthCalledWith(
      call,
      "Pit Officer: new alert",
      { body: "Paper and live access: Desk (desk)", tag: "second" },
    );
  }
});

it("keeps the baseline across a remount and StrictMode in one tab", () => {
  const first = render(
    <StrictMode>
      <Panel load={ready(initial)} />
    </StrictMode>,
  );
  expect(BrowserNotification.attempts).not.toHaveBeenCalled();
  first.unmount();

  render(
    <StrictMode>
      <Panel load={ready(initial, newAlert)} />
    </StrictMode>,
  );
  expect(BrowserNotification.attempts).toHaveBeenCalledExactlyOnceWith(
    "Pit Officer: new alert",
    { body: "Paper and live access: Desk (desk)", tag: "second" },
  );
});

it.each(["probe TypeError", "delivery TypeError"])(
  "shows unavailable notifications in the drawer for %s",
  async (scenario) => {
    BrowserNotification.permission =
      scenario === "delivery TypeError" ? "granted" : "default";
    BrowserNotification.attempts.mockImplementation(() => {
      throw new TypeError("Illegal constructor");
    });

    const { launcher, state } = await mountLauncher();
    if (scenario === "delivery TypeError") {
      expect(BrowserNotification.attempts).not.toHaveBeenCalled();
    }
    state.alerts = [newAlert];
    await pollAlerts();
    await act(async () => {
      fireEvent.click(launcher);
    });

    const drawer = within(screen.getByRole("dialog"));
    expect(
      drawer.getByText(
        "Browser notifications are not available in this browser",
      ),
    ).toBeVisible();
    expect(
      drawer.queryByRole("button", { name: "Allow notifications" }),
    ).not.toBeInTheDocument();
    expect(
      drawer.queryByText("Browser notifications failed"),
    ).not.toBeInTheDocument();
    expect(document.title).toBe("(1) Pit Officer");
    expect(BrowserNotification.attempts).toHaveBeenCalledTimes(1);
  },
);

function clickNotification(notification: BrowserNotification) {
  return act(async () => {
    notification.dispatchEvent(new Event("click"));
  });
}

it("opens and marks a notified alert, keeps it expanded on another notification click, and lets the row collapse", async () => {
  const { focus, markRead, state } = await mountLauncher();
  state.alerts = [notifiedAlert];
  await pollAlerts();
  expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
  expect(BrowserNotification.shown).toHaveLength(1);
  const [notification] = BrowserNotification.shown;

  await clickNotification(notification);
  const drawer = screen.getByRole("dialog");
  const row = within(drawer).getByRole("button", { name: /New alert/ });
  expect(focus).toHaveBeenCalledTimes(1);
  expect(notification.close).toHaveBeenCalledTimes(1);
  expect(row).toHaveAttribute("aria-expanded", "true");
  expect(markRead).toHaveBeenCalledExactlyOnceWith(
    "/app/api/v1/alerts/second/read",
    "PUT",
    JSON.stringify({ read: true }),
  );
  expect(
    within(drawer).getByRole("button", { name: "Mark unread" }),
  ).toBeVisible();

  await clickNotification(notification);
  expect(row).toHaveAttribute("aria-expanded", "true");
  expect(markRead).toHaveBeenCalledTimes(1);

  fireEvent.click(row);
  expect(row).toHaveAttribute("aria-expanded", "false");
  expect(markRead).toHaveBeenCalledTimes(1);
});

it("opens an already read notified alert without writing a read mark", async () => {
  const { markRead, state } = await mountLauncher();
  state.alerts = [notifiedAlert];
  await pollAlerts();
  const [notification] = BrowserNotification.shown;

  state.alerts = [{ ...notifiedAlert, read: true }];
  await pollAlerts();
  await clickNotification(notification);

  const drawer = screen.getByRole("dialog");
  expect(
    within(drawer).getByRole("button", { name: /New alert/ }),
  ).toHaveAttribute("aria-expanded", "true");
  expect(markRead).not.toHaveBeenCalled();
});

it("opens only the drawer if the notified alert is no longer listed", async () => {
  const { markRead, state } = await mountLauncher();
  state.alerts = [notifiedAlert];
  await pollAlerts();
  const [notification] = BrowserNotification.shown;

  state.alerts = [{ ...initial, read: true }];
  await pollAlerts();
  await clickNotification(notification);

  const drawer = screen.getByRole("dialog");
  expect(
    within(drawer).getByRole("button", { name: /access: Desk/ }),
  ).toHaveAttribute("aria-expanded", "false");
  expect(markRead).not.toHaveBeenCalled();
});

it("opens and marks a notified alert after recovering from a failed reload", async () => {
  const { launcher, markRead, state } = await mountLauncher();
  state.alerts = [notifiedAlert];
  await pollAlerts();
  const [notification] = BrowserNotification.shown;

  state.loadError = true;
  await act(async () => {
    fireEvent.click(launcher);
  });
  const drawer = screen.getByRole("dialog");
  expect(within(drawer).getByRole("button", { name: "Retry" })).toBeVisible();

  state.loadError = false;
  await clickNotification(notification);

  expect(
    within(drawer).getByRole("button", { name: /New alert/ }),
  ).toHaveAttribute("aria-expanded", "true");
  expect(markRead).toHaveBeenCalledExactlyOnceWith(
    "/app/api/v1/alerts/second/read",
    "PUT",
    JSON.stringify({ read: true }),
  );
});

it("drops a pending notification click when the drawer closes", async () => {
  const { launcher, markRead, state } = await mountLauncher();
  state.alerts = [notifiedAlert];
  await pollAlerts();
  const [notification] = BrowserNotification.shown;

  state.loadError = true;
  await act(async () => {
    fireEvent.click(launcher);
  });
  const drawer = screen.getByRole("dialog");
  expect(within(drawer).getByRole("button", { name: "Retry" })).toBeVisible();

  await clickNotification(notification);
  expect(within(drawer).getByRole("button", { name: "Retry" })).toBeVisible();
  fireEvent.click(within(drawer).getByRole("button", { name: "Close" }));
  expect(screen.queryByRole("dialog")).toBeNull();

  state.loadError = false;
  await pollAlerts();
  expect(launcher).toHaveAccessibleName(/Unresolved: 1/);
  expect(markRead).not.toHaveBeenCalled();

  await act(async () => {
    fireEvent.click(launcher);
  });
  expect(
    within(screen.getByRole("dialog")).getByRole("button", {
      name: /New alert/,
    }),
  ).toHaveAttribute("aria-expanded", "false");
});

it("keeps an alert opened by a notification expanded when a different alert resolves", async () => {
  const { acknowledge, launcher, state } = await mountLauncher();
  let finishResolve!: () => void;
  acknowledge.mockReturnValue(
    new Promise<void>((resolve) => {
      finishResolve = resolve;
    }),
  );
  state.alerts = [{ ...initial, read: true }, notifiedAlert];
  await pollAlerts();
  const [notification] = BrowserNotification.shown;

  await act(async () => {
    fireEvent.click(launcher);
  });
  const drawer = screen.getByRole("dialog");
  fireEvent.click(within(drawer).getByRole("button", { name: /access: Desk/ }));
  await act(async () => {
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Acknowledge" }),
    );
  });
  expect(acknowledge).toHaveBeenCalledTimes(1);

  await clickNotification(notification);
  const row = within(drawer).getByRole("button", { name: /New alert/ });
  expect(row).toHaveAttribute("aria-expanded", "true");

  await act(async () => {
    finishResolve();
  });
  expect(
    within(drawer).queryByRole("button", { name: /access: Desk/ }),
  ).toBeNull();
  expect(row).toHaveAttribute("aria-expanded", "true");
});
