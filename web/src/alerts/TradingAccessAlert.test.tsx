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
import { beforeEach, expect, it, vi } from "vitest";
import i18n from "@/i18n";
import type { ActionableAlert } from "./registry";
import { Summary, Detail } from "./TradingAccessAlert";

const alert: ActionableAlert = {
  id: "event",
  type: "mixed_trading_access",
  at: "2026-11-16T00:00:00Z",
  source: "panel",
  payload: { account: "desk", title: "<script>Desk</script>" },
  read: false,
  actions: ["acknowledge"],
  deliveries: [],
};
beforeEach(async () => {
  await i18n.changeLanguage("en");
});
it("names the account safely and acknowledges using only the registered action", async () => {
  const resolve = vi.fn().mockResolvedValue(undefined);
  const { container } = render(
    <I18nextProvider i18n={i18n}>
      <Summary alert={alert} />
      <Detail alert={alert} pending={false} resolve={resolve} />
    </I18nextProvider>,
  );
  expect(screen.getByText(/Paper and live access:/)).toHaveTextContent(
    "<script>Desk</script> (desk)",
  );
  expect(container.querySelector("script")).toBeNull();
  await userEvent
    .setup()
    .click(screen.getByRole("button", { name: "Acknowledge" }));
  expect(resolve).toHaveBeenCalledExactlyOnceWith("acknowledge", {});
});
it("offers no unregistered action", () => {
  render(
    <I18nextProvider i18n={i18n}>
      <Detail
        alert={{ ...alert, actions: [] }}
        pending={false}
        resolve={vi.fn()}
      />
    </I18nextProvider>,
  );
  expect(screen.queryByRole("button")).not.toBeInTheDocument();
});
