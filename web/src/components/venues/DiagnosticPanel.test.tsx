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

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { Diagnostic } from "@/api/types";
import {
  DiagnosticPanel,
  type DiagnosticPanelProps,
} from "@/components/venues";
import i18n from "@/i18n";
import { formatDateTime } from "@/i18n/format";

const DANGER = "border-[var(--danger)]";
const WARN = "border-[var(--warn)]";
const NEUTRAL = "border-[var(--tag-border)]";

function diagnostic(overrides: Partial<Diagnostic> = {}): Diagnostic {
  return {
    level: "error",
    code: "bad-symbol",
    kind: "config",
    title: "Bad symbol",
    detail: "BTCUSD is not valid for this feed.",
    actions: [],
    at: "2026-06-22T16:05:00Z",
    ...overrides,
  };
}

function panel(props: DiagnosticPanelProps) {
  return (
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <DiagnosticPanel {...props} />
      </MemoryRouter>
    </I18nextProvider>
  );
}

function renderPanel(props: DiagnosticPanelProps) {
  return render(panel(props));
}

function groupOf(title: string): HTMLElement {
  const heading = screen.getByText(title);
  if (!heading.parentElement) throw new Error(`no group for ${title}`);
  return heading.parentElement;
}

function toolbar() {
  return within(screen.getByRole("toolbar", { name: "Problem actions" }));
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("DiagnosticPanel", () => {
  it("groups config problems apart from environment and provider problems", () => {
    renderPanel({
      items: [
        diagnostic({ kind: "config", title: "Bad symbol" }),
        diagnostic({
          kind: "environment",
          code: "clock",
          title: "Clock skew",
        }),
        diagnostic({
          kind: "provider",
          code: "connect",
          title: "Connection error",
        }),
      ],
    });

    const region = screen.getByRole("region", { name: "Problems" });
    expect(region).toHaveClass("max-h-96", "overflow-y-auto", "pr-1");
    const actionNeeded = within(groupOf("Action needed"));
    expect(actionNeeded.getByText("Bad symbol")).toBeInTheDocument();
    expect(actionNeeded.queryByText("Clock skew")).not.toBeInTheDocument();
    expect(
      actionNeeded.queryByText("Connection error"),
    ).not.toBeInTheDocument();
    const provider = within(groupOf("Connection / provider"));
    expect(provider.getByText("Clock skew")).toBeInTheDocument();
    expect(provider.getByText("Connection error")).toBeInTheDocument();
    expect(provider.queryByText("Bad symbol")).not.toBeInTheDocument();
  });

  it("omits a group that has no problems", () => {
    renderPanel({
      items: [diagnostic({ kind: "provider", title: "Connection error" })],
    });

    expect(screen.queryByText("Action needed")).not.toBeInTheDocument();
    expect(screen.getByText("Connection / provider")).toBeInTheDocument();
  });

  it("renders nothing without problems", () => {
    const { container } = renderPanel({
      items: [],
      references: { docsUrl: "https://docs.example.com" },
      onRestart: vi.fn(),
      onRemoveInstrument: vi.fn(),
    });

    expect(container).toBeEmptyDOMElement();
  });

  it("shows each problem's level, instrument, time, detail and fix", () => {
    renderPanel({
      items: [
        diagnostic({
          level: "error",
          code: "e",
          title: "Failure",
          instrument: "BTCUSD",
          remediation: "Remove the symbol.",
        }),
        diagnostic({ level: "warn", code: "w", title: "Slow" }),
        diagnostic({ level: "info", code: "i", title: "Note" }),
      ],
    });

    expect(screen.getByText("error")).toHaveClass(DANGER);
    expect(screen.getByText("warn")).toHaveClass(WARN);
    expect(screen.getByText("info")).toHaveClass(NEUTRAL);
    expect(screen.getByText("BTCUSD")).toHaveClass("font-mono");
    expect(
      screen.getAllByText(formatDateTime("2026-06-22T16:05:00Z")),
    ).toHaveLength(3);
    expect(
      screen.getAllByText("BTCUSD is not valid for this feed."),
    ).toHaveLength(3);
    expect(screen.getAllByText("How to fix:")).toHaveLength(1);
    expect(screen.getByText("Remove the symbol.")).toBeInTheDocument();
  });

  it.each([
    { levels: ["warn", "error", "info"], variant: DANGER },
    { levels: ["info", "warn"], variant: WARN },
    { levels: ["info"], variant: NEUTRAL },
  ])(
    "marks the summary by the worst level of $levels",
    ({ levels, variant }) => {
      renderPanel({
        items: levels.map((level) => diagnostic({ level, code: level })),
      });

      expect(screen.getByText(String(levels.length))).toHaveClass(variant);
      expect(screen.getByText(`${levels.length} active`)).toBeInTheDocument();
    },
  );

  it("collapses to a summary button and expands back", async () => {
    const user = userEvent.setup();
    renderPanel({
      items: [
        diagnostic({ code: "a" }),
        diagnostic({ code: "b", level: "warn" }),
      ],
    });

    const collapse = screen.getByRole("button", { name: "Collapse" });
    expect(collapse).toHaveAttribute("aria-expanded", "true");
    await user.click(collapse);

    expect(
      screen.queryByRole("region", { name: "Problems" }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
    const expand = screen.getByRole("button", { name: /Problems: 2/ });
    expect(expand).toHaveAttribute("aria-expanded", "false");
    expect(within(expand).getByText("2")).toHaveClass(DANGER);
    expect(within(expand).getByText("Open")).toBeInTheDocument();

    await user.click(expand);

    expect(
      screen.getByRole("region", { name: "Problems" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: "Collapse" }),
    ).toBeInTheDocument();
  });

  it("opens a new problem set expanded after the problems cleared", async () => {
    const user = userEvent.setup();
    const { container, rerender } = renderPanel({
      items: [diagnostic({ code: "a", title: "Bad symbol" })],
    });
    await user.click(screen.getByRole("button", { name: "Collapse" }));
    expect(
      screen.queryByRole("region", { name: "Problems" }),
    ).not.toBeInTheDocument();

    rerender(panel({ items: [] }));
    expect(container).toBeEmptyDOMElement();

    rerender(
      panel({
        items: [
          diagnostic({ kind: "provider", code: "b", title: "Clock skew" }),
        ],
      }),
    );

    const region = screen.getByRole("region", { name: "Problems" });
    expect(within(region).getByText("Clock skew")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Collapse" })).toHaveAttribute(
      "aria-expanded",
      "true",
    );
  });

  it("offers restart only when the owner can restart, once per panel", async () => {
    const user = userEvent.setup();
    const onRestart = vi.fn();
    const items = [
      diagnostic({ code: "a", actions: [{ type: "restart" }] }),
      diagnostic({ code: "b", actions: [{ type: "restart" }] }),
    ];
    const { unmount } = renderPanel({ items, onRestart });

    const restart = toolbar().getAllByRole("button", {
      name: "Restart feeds",
    });
    expect(restart).toHaveLength(1);
    await user.click(restart[0]);
    expect(onRestart).toHaveBeenCalledTimes(1);
    unmount();

    renderPanel({ items });
    expect(
      toolbar().queryByRole("button", { name: "Restart feeds" }),
    ).not.toBeInTheDocument();
  });

  it("offers instrument removal only with a handler and a target", async () => {
    const user = userEvent.setup();
    const onRemoveInstrument = vi.fn();
    const items = [
      diagnostic({
        actions: [
          { type: "remove_instrument", target: "BTCUSD" },
          { type: "remove_instrument" },
        ],
      }),
    ];
    const { unmount } = renderPanel({ items, onRemoveInstrument });

    const remove = toolbar().getAllByRole("button", { name: /^Remove/ });
    expect(remove).toHaveLength(1);
    expect(remove[0]).toHaveTextContent("Remove BTCUSD");
    await user.click(remove[0]);
    expect(onRemoveInstrument).toHaveBeenCalledWith("BTCUSD");
    unmount();

    renderPanel({ items });
    expect(
      toolbar().queryByRole("button", { name: /^Remove/ }),
    ).not.toBeInTheDocument();
  });

  it("links provider docs and symbols only when their URLs are given", () => {
    const items = [
      diagnostic({
        actions: [{ type: "open_docs" }, { type: "open_symbols" }],
      }),
    ];
    const { unmount } = renderPanel({
      items,
      references: {
        docsUrl: "https://docs.example.com",
        symbolsUrl: "https://symbols.example.com",
      },
    });

    const docs = toolbar().getByRole("link", { name: "Provider docs" });
    expect(docs).toHaveAttribute("href", "https://docs.example.com");
    expect(docs).toHaveAttribute("target", "_blank");
    expect(docs).toHaveAttribute("rel", "noopener noreferrer");
    const symbols = toolbar().getByRole("link", { name: "Valid symbols" });
    expect(symbols).toHaveAttribute("href", "https://symbols.example.com");
    expect(symbols).toHaveAttribute("target", "_blank");
    unmount();

    renderPanel({ items, references: {} });
    expect(toolbar().queryByText("Provider docs")).not.toBeInTheDocument();
    expect(toolbar().queryByText("Valid symbols")).not.toBeInTheDocument();
  });

  it("disables action buttons while busy", () => {
    renderPanel({
      items: [
        diagnostic({
          actions: [
            { type: "restart" },
            { type: "remove_instrument", target: "BTCUSD" },
          ],
        }),
      ],
      busy: true,
      onRestart: vi.fn(),
      onRemoveInstrument: vi.fn(),
    });

    expect(
      toolbar().getByRole("button", { name: "Restart feeds" }),
    ).toBeDisabled();
    expect(
      toolbar().getByRole("button", { name: "Remove BTCUSD" }),
    ).toBeDisabled();
  });

  it("always links the service logs", () => {
    renderPanel({ items: [diagnostic()] });

    expect(
      toolbar().getByRole("link", { name: "Service logs" }),
    ).toHaveAttribute("href", "/service#logs");
  });
});
