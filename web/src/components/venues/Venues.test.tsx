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
import { beforeEach, describe, expect, it, vi } from "vitest";

import {
  AddProviderButton,
  ProviderGuideDialog,
  ProviderIcon,
  ProviderTile,
  VenueCardHeader,
  type VenueProviderOption,
} from "@/components/venues";
import i18n from "@/i18n";

const provider: VenueProviderOption = {
  type: "alpaca",
  title: "Alpaca connection",
  description: "Connect to the broker.",
  siteUrl: "https://example.com",
  docsUrl: "https://example.com/docs",
  symbolsUrl: "https://example.com/symbols",
};

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("venue blocks", () => {
  it("maps every provider type to its icon and preserves sizing and unknown icons", () => {
    const brands = [
      ["ib", "#050608"],
      ["binance", "#0b0e11"],
      ["kraken", "#fff"],
      ["coinbase", "#0052ff"],
      ["alpaca", "#ffd200"],
      ["okx", "#000"],
      ["bybit", "#151827"],
      ["oanda", "#fff"],
      ["finnhub", "#fff"],
    ];
    const { container, rerender } = render(
      <ProviderIcon type="ib" className="h-5 w-5" />,
    );
    const markup = new Set<string>();
    for (const [type, fill] of brands) {
      rerender(<ProviderIcon type={type} className="h-5 w-5" />);
      const svg = container.querySelector("svg")!;
      expect(svg).toHaveAttribute("aria-hidden", "true");
      expect(svg).toHaveClass("h-5", "w-5");
      expect(svg.querySelector("rect")).toHaveAttribute("fill", fill);
      if (type === "kraken") {
        expect(svg.querySelector("path")).toHaveAttribute("fill", "#5841d8");
      }
      if (type === "finnhub") {
        expect(svg.querySelector("path")).toHaveAttribute("fill", "#37b34a");
      }
      if (type === "oanda") {
        expect(svg.querySelector("text")).toHaveTextContent("ANDA");
      }
      markup.add(svg.innerHTML);
    }
    expect(markup.size).toBe(brands.length);

    for (const [type, iconClass] of [
      ["mock", "lucide-flask-conical"],
      ["byo", "lucide-upload"],
      ["unknown", "lucide-briefcase-business"],
    ]) {
      rerender(<ProviderIcon type={type} className="h-5 w-5" />);
      expect(container.querySelector("svg")).toHaveClass(
        iconClass,
        "h-5",
        "w-5",
      );
    }
  });

  it("renders a tile from its props and selects the original entry", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const { container } = render(
      <ProviderTile provider={provider} onSelect={onSelect} />,
    );

    expect(screen.getByText(provider.title)).toHaveClass(
      "font-semibold",
      "text-text",
    );
    expect(screen.getByText(provider.description!)).toHaveClass(
      "mt-1",
      "text-xs",
      "text-muted",
    );
    expect(container.querySelector("svg")).toHaveClass(
      "mt-0.5",
      "h-5",
      "w-5",
      "shrink-0",
      "text-accent",
    );
    for (const [label, href] of [
      ["Site", provider.siteUrl],
      ["Docs", provider.docsUrl],
      ["Symbols", provider.symbolsUrl],
    ]) {
      const link = screen.getByRole("link", { name: label });
      expect(link).toHaveAttribute("href", href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
      expect(link.parentElement).toHaveClass("border-t", "px-4", "py-2");
    }
    await user.click(screen.getByRole("button", { name: /Alpaca connection/ }));
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(provider);
  });

  it("shows only supplied tile links and distinguishes absent and empty descriptions", () => {
    const entry = { type: "alpaca", title: "Alpaca" };
    const { container, rerender } = render(
      <ProviderTile provider={entry} onSelect={vi.fn()} />,
    );
    expect(container.querySelectorAll("p")).toHaveLength(1);
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(container.querySelector(".border-t")).not.toBeInTheDocument();

    for (const [key, label] of [
      ["siteUrl", "Site"],
      ["docsUrl", "Docs"],
      ["symbolsUrl", "Symbols"],
    ]) {
      rerender(
        <ProviderTile
          provider={{ ...entry, description: "", [key]: "https://example.com" }}
          onSelect={vi.fn()}
        />,
      );
      expect(container.querySelectorAll("p")).toHaveLength(2);
      expect(container.querySelectorAll("p")[1]).toBeEmptyDOMElement();
      expect(screen.getAllByRole("link")).toHaveLength(1);
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        "https://example.com",
      );
    }
  });

  it("renders the provider tiles behind the dialog and selects one", async () => {
    const user = userEvent.setup();
    const onSelect = vi.fn();
    const finnhub = {
      type: "finnhub",
      title: "Finnhub",
      description: "Stock prices.",
    };
    render(
      <I18nextProvider i18n={i18n}>
        <ProviderGuideDialog
          open
          title="Available sources"
          description="Choose a provider."
          providers={[
            finnhub,
            { type: "binance", title: "Binance", siteUrl: provider.siteUrl },
          ]}
          onSelect={onSelect}
          onOpenChange={vi.fn()}
        />
      </I18nextProvider>,
    );

    const dialog = screen.getByRole("dialog", { name: "Available sources" });
    expect(dialog).toHaveClass(
      "max-h-[90vh]",
      "max-w-[40rem]",
      "overflow-y-auto",
    );
    expect(dialog).toHaveAccessibleDescription("Choose a provider.");
    expect(within(dialog).getByText("Finnhub")).toBeInTheDocument();
    expect(within(dialog).getByText("Stock prices.")).toBeInTheDocument();
    expect(within(dialog).getByText("Binance")).toBeInTheDocument();
    expect(within(dialog).getAllByRole("link")).toHaveLength(1);
    expect(within(dialog).getByRole("link", { name: "Site" })).toHaveAttribute(
      "href",
      provider.siteUrl,
    );

    await user.click(within(dialog).getByRole("button", { name: /Finnhub/ }));
    expect(onSelect).toHaveBeenCalledExactlyOnceWith(finnhub);
  });

  it("obeys the dialog open prop and reports closing through onOpenChange", async () => {
    const user = userEvent.setup();
    const onOpenChange = vi.fn();
    const props = {
      title: "Available sources",
      description: "Choose a provider.",
      providers: [],
      onSelect: vi.fn(),
      onOpenChange,
    };
    const { rerender } = render(
      <ProviderGuideDialog {...props} open={false} />,
    );
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
    rerender(<ProviderGuideDialog {...props} open />);
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.keyboard("{Escape}");
    expect(onOpenChange).toHaveBeenCalledExactlyOnceWith(false);
  });

  it("renders one icon per supplied type, the count, and the add action", async () => {
    const user = userEvent.setup();
    const onClick = vi.fn();
    const { rerender } = render(
      <AddProviderButton
        label="Add connection"
        providerTypes={["alpaca", "mock", "byo"]}
        onClick={onClick}
      />,
    );
    const button = screen.getByRole("button", { name: /Add connection/ });
    expect(button).toHaveAttribute("type", "button");
    expect(button).toHaveClass("w-full", "rounded-card", "px-4", "py-3");
    expect(within(button).getByText("3 providers")).toBeInTheDocument();
    const icons = button.querySelectorAll("span.ml-1 > svg");
    expect(icons).toHaveLength(3);
    expect(icons[0].querySelector("rect")).toHaveAttribute("fill", "#ffd200");
    expect(icons[1]).toHaveClass("lucide-flask-conical");
    expect(icons[2]).toHaveClass("lucide-upload");
    for (const icon of icons) {
      expect(icon).toHaveClass("h-4", "w-4", "shrink-0");
    }
    await user.click(button);
    expect(onClick).toHaveBeenCalledTimes(1);

    rerender(
      <AddProviderButton
        label="Add connection"
        providerTypes={[]}
        onClick={onClick}
      />,
    );
    expect(screen.getByText("0 providers")).toBeInTheDocument();
    expect(button.querySelectorAll("span.ml-1 > svg")).toHaveLength(0);
  });

  it.each([
    ["en", 1, "1 provider"],
    ["en", 2, "2 providers"],
    ["en", 11, "11 providers"],
    ["ru", 1, "Провайдеров: 1"],
    ["ru", 2, "Провайдеров: 2"],
    ["ru", 5, "Провайдеров: 5"],
    ["ru", 11, "Провайдеров: 11"],
    ["ru", 21, "Провайдеров: 21"],
    ["zh-CN", 1, "1 个提供方"],
    ["zh-CN", 2, "2 个提供方"],
    ["zh-CN", 11, "11 个提供方"],
  ] as const)(
    "renders the %s provider count %i as %s",
    async (language, count, text) => {
      await i18n.changeLanguage(language);
      render(
        <AddProviderButton
          label="Add connection"
          providerTypes={Array.from(
            { length: count },
            (_, index) => `provider-${index}`,
          )}
          onClick={vi.fn()}
        />,
      );
      expect(screen.getByText(text, { exact: true })).toBeInTheDocument();
    },
  );

  it("renders the header title ID, description, badges, error, links, and actions", async () => {
    const user = userEvent.setup();
    const onAction = vi.fn();
    const { container } = render(
      <VenueCardHeader
        providerType="alpaca"
        title="Trading account"
        titleId="trading-account-title"
        description="Account connection."
        badges={
          <>
            <span>Paper</span>
            <span>Live</span>
          </>
        }
        error="Connection failed."
        links={provider}
        actions={<button onClick={onAction}>Settings</button>}
      />,
    );
    const title = screen.getByRole("heading", {
      name: "Trading account",
      level: 3,
    });
    expect(title).toHaveAttribute("id", "trading-account-title");
    expect(title).toHaveClass("truncate");
    expect(container.firstElementChild).toHaveClass(
      "flex-row",
      "items-center",
      "justify-between",
      "gap-3",
    );
    expect(container.querySelector("svg")).toHaveClass(
      "mt-0.5",
      "h-5",
      "w-5",
      "shrink-0",
      "text-accent",
    );
    expect(screen.getByText("Account connection.")).toHaveClass(
      "mt-1",
      "max-w-3xl",
      "text-xs",
      "text-muted",
    );
    expect(screen.getByText("Paper").parentElement).toHaveClass(
      "mt-1",
      "flex",
      "flex-wrap",
      "items-center",
      "gap-2",
    );
    expect(screen.getByText("Live")).toBeInTheDocument();
    expect(screen.getByText("Connection failed.")).toHaveClass(
      "mt-1.5",
      "break-words",
      "text-xs",
      "text-[var(--danger)]",
    );
    for (const [label, href] of [
      ["Provider site", provider.siteUrl],
      ["Provider docs", provider.docsUrl],
      ["Valid symbols", provider.symbolsUrl],
    ]) {
      const link = screen.getByRole("link", { name: label });
      expect(link).toHaveAttribute("href", href);
      expect(link).toHaveAttribute("target", "_blank");
      expect(link).toHaveAttribute("rel", "noopener noreferrer");
      expect(link.parentElement).toHaveClass(
        "mt-1.5",
        "flex",
        "flex-wrap",
        "gap-3",
      );
    }
    const action = screen.getByRole("button", { name: "Settings" });
    expect(action.parentElement).toHaveClass("flex", "shrink-0", "gap-2");
    await user.click(action);
    expect(onAction).toHaveBeenCalledTimes(1);
  });

  it("omits absent header content, preserves empty descriptions, and shows only supplied links", () => {
    const props = { providerType: "alpaca", title: "Trading account" };
    const { container, rerender } = render(<VenueCardHeader {...props} />);
    expect(container.querySelector("p")).not.toBeInTheDocument();
    expect(screen.queryByRole("link")).not.toBeInTheDocument();
    expect(container.querySelector(".mt-1")).not.toBeInTheDocument();
    expect(container.querySelector(".mt-1\\.5")).not.toBeInTheDocument();
    expect(screen.getByRole("heading")).not.toHaveAttribute("id");

    for (const [key, label] of [
      ["siteUrl", "Provider site"],
      ["docsUrl", "Provider docs"],
      ["symbolsUrl", "Valid symbols"],
    ]) {
      rerender(
        <VenueCardHeader
          {...props}
          description=""
          links={{ [key]: "https://example.com" }}
        />,
      );
      expect(container.querySelectorAll("p")).toHaveLength(1);
      expect(container.querySelector("p")).toBeEmptyDOMElement();
      expect(screen.getAllByRole("link")).toHaveLength(1);
      expect(screen.getByRole("link", { name: label })).toHaveAttribute(
        "href",
        "https://example.com",
      );
    }
    rerender(<VenueCardHeader {...props} error="Connection failed." />);
    expect(screen.getByText("Connection failed.").tagName).toBe("P");
    expect(screen.getByText("Connection failed.")).toHaveClass(
      "text-[var(--danger)]",
    );
    rerender(<VenueCardHeader {...props} error="" />);
    expect(container.querySelector("p")).not.toBeInTheDocument();
    for (const badges of [undefined, null, false, true, ""]) {
      rerender(<VenueCardHeader {...props} badges={badges} />);
      expect(container.querySelector(".mt-1")).not.toBeInTheDocument();
      expect(container.querySelector("p")).not.toBeInTheDocument();
    }
  });
});
