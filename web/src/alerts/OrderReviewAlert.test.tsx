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
import { I18nextProvider } from "react-i18next";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { ActionableAlert } from "./registry";
import { summary, Detail } from "./OrderReviewAlert";

const alert: ActionableAlert = {
  id: "order/1",
  type: "order_awaiting_review",
  at: "2026-11-20T00:00:00Z",
  source: "panel",
  payload: {
    order: "order/1",
    account: "<script>desk</script>",
    markCode: "size",
  },
  read: false,
  actions: ["close"],
  deliveries: [],
};

function Location() {
  const location = useLocation();
  return <output>{location.pathname + location.search}</output>;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

it("shows identifiers safely, opens the order and closes with empty input", async () => {
  const resolve = vi.fn().mockResolvedValue(undefined);
  const { container } = render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter initialEntries={["/orders"]}>
        <span>{summary(alert, i18n.t)}</span>
        <Detail alert={alert} pending={false} resolve={resolve} />
        <Location />
      </MemoryRouter>
    </I18nextProvider>,
  );
  expect(
    screen.getByText("Order awaiting review: order/1"),
  ).toBeInTheDocument();
  expect(
    screen.getByText(
      "Order order/1, account <script>desk</script>, mark size.",
    ),
  ).toBeInTheDocument();
  expect(container.querySelector("script")).toBeNull();
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: "Open order" }));
  expect(screen.getByRole("status")).toHaveTextContent(
    "/orders?order=order%2F1",
  );
  expect(resolve).not.toHaveBeenCalled();
  await user.click(screen.getByRole("button", { name: "Close" }));
  expect(resolve).toHaveBeenCalledExactlyOnceWith("close", {});
});

it("offers only registered close actions and disables close while pending", () => {
  const resolve = vi.fn();
  const { rerender } = render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <Detail alert={alert} pending resolve={resolve} />
      </MemoryRouter>
    </I18nextProvider>,
  );
  expect(screen.getByRole("button", { name: "Close" })).toBeDisabled();
  rerender(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <Detail
          alert={{ ...alert, actions: [] }}
          pending={false}
          resolve={resolve}
        />
      </MemoryRouter>
    </I18nextProvider>,
  );
  expect(
    screen.queryByRole("button", { name: "Close" }),
  ).not.toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Open order" })).toBeEnabled();
});

it.each([null, {}, { order: "order/1", account: "desk", markCode: 42 }])(
  "rejects malformed payload %j without offering actions",
  (payload) => {
    render(
      <I18nextProvider i18n={i18n}>
        <MemoryRouter>
          <Detail
            alert={{ ...alert, payload }}
            pending={false}
            resolve={vi.fn()}
          />
        </MemoryRouter>
      </I18nextProvider>,
    );
    expect(
      screen.getByText("The order review alert has an invalid payload."),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  },
);
