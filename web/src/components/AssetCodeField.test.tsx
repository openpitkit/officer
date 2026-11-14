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

import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { AssetCodeField, type OfficerApi } from "@/framework";
import { renderWithApi } from "@/test/apiClient";

const fetchAssets = vi.fn<OfficerApi["fetchAssets"]>();

function Field({ disabled = false }: { disabled?: boolean }) {
  const [value, setValue] = useState("");
  return (
    <AssetCodeField
      id="asset-mask"
      label="Asset mask"
      value={value}
      onChange={setValue}
      disabled={disabled}
    />
  );
}

describe("AssetCodeField", () => {
  beforeEach(() => {
    fetchAssets.mockReset().mockResolvedValue([
      { code: "BTC", title: "", assetClass: "" },
      { code: "BTCEUR", title: "", assetClass: "" },
    ]);
  });

  it("selects a standard asset suggestion and still accepts wildcard masks", async () => {
    const user = userEvent.setup();
    renderWithApi(<Field />, { api: { fetchAssets } });
    const input = screen.getByRole("combobox", { name: "Asset mask" });
    await user.type(input, "BT");
    await user.click(await screen.findByRole("option", { name: "BTC" }));
    expect(input).toHaveValue("BTC");
    await user.type(input, "*");
    expect(input).toHaveValue("BTC*");
    await user.click(screen.getByRole("button", { name: "Clear field" }));
    expect(input).toHaveValue("");
  });

  it("keeps a freely typed mask when suggestions fail", async () => {
    fetchAssets.mockRejectedValue(new Error("Asset lookup failed"));
    const user = userEvent.setup();
    renderWithApi(<Field />, { api: { fetchAssets } });
    await user.type(
      screen.getByRole("combobox", { name: "Asset mask" }),
      "BTC*",
    );
    expect(screen.getByRole("combobox")).toHaveValue("BTC*");
    expect(await screen.findByRole("status")).toHaveTextContent(
      "Asset suggestions could not be loaded.",
    );
  });

  it("disables editing and suggestion requests with its owner", () => {
    renderWithApi(<Field disabled />, { api: { fetchAssets } });
    expect(screen.getByRole("combobox")).toBeDisabled();
    expect(fetchAssets).not.toHaveBeenCalled();
  });
});
