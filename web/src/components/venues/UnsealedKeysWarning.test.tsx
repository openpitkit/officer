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
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it } from "vitest";

import type { CredentialStorage } from "@/api/types";
import type { LoadState } from "@/api/usePolling";
import { UnsealedKeysWarning } from "@/components/venues";
import i18n from "@/i18n";

function ready(credentialsSealed: boolean): LoadState<CredentialStorage> {
  return {
    state: "ready",
    data: {
      credentialsSealed,
      plaintextTradingKeys: false,
      plaintextMarketDataKeys: false,
      sealedAt: null,
      resealedSecrets: 0,
    },
    error: null,
  };
}

function renderWarning(storage: LoadState<CredentialStorage>) {
  return render(
    <I18nextProvider i18n={i18n}>
      <MemoryRouter>
        <UnsealedKeysWarning storage={storage} />
      </MemoryRouter>
    </I18nextProvider>,
  );
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("UnsealedKeysWarning", () => {
  it("announces plain-text keys with the master-key guide and its caution", () => {
    renderWarning(ready(false));
    const alert = within(screen.getByRole("alert"));
    expect(
      alert.getByText("Keys will be stored unencrypted"),
    ).toBeInTheDocument();
    expect(
      alert.getByText(
        "Officer runs without a master key, so these keys are written to its database in plain text. Anyone who can read the database or a backup can use them, including to trade where a key allows it. Set a master key to store them sealed.",
      ),
    ).toBeInTheDocument();
    expect(
      alert.getByText(
        "A lost master key makes the database unopenable - back the key up before you set it.",
      ),
    ).toBeInTheDocument();
    expect(
      alert.getByRole("link", { name: "How to set a master key" }),
    ).toBeInTheDocument();
  });

  it("reports a failed storage load instead of staying silent", () => {
    renderWarning({
      state: "error",
      data: null,
      error: "credential storage unavailable",
    });
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not check whether stored keys are encrypted: credential storage unavailable",
    );
    expect(
      screen.queryByText("Keys will be stored unencrypted"),
    ).not.toBeInTheDocument();
  });

  it("says the check is running while storage is loading", () => {
    renderWarning({ state: "loading", data: null, error: null });
    expect(screen.getByRole("status")).toHaveTextContent(
      "Checking whether stored keys are encrypted…",
    );
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("renders nothing when sealed", () => {
    const { container } = renderWarning(ready(true));
    expect(container).toBeEmptyDOMElement();
  });
});
