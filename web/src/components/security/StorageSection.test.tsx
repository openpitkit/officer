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

import { screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AccessMode } from "@/api/access";
import type { CredentialStorage, SigningKeysStatus } from "@/api/types";
import {
  AccessSection,
  AccessTokensProvider,
} from "@/components/security/AccessSection";
import { StorageSection } from "@/components/security/StorageSection";
import i18n from "@/i18n";
import { formatDateTime } from "@/i18n/format";
import { setAccountScope } from "@/lib/accountScope";
import { setPanelSession } from "@/lib/panelSession";
import {
  errorResponse,
  jsonResponse,
  renderWithApi,
  serving,
  type Route,
} from "@/test/apiClient";

const API = "/app/api/v1";
const SEALED_AT = "2026-10-15T14:05:00Z";
const FINGERPRINT = "3f9a51c0d2e4c21e";
const AGENT_TOKEN = "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w";
const API_TOKEN = "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w";
const NEW_AGENT_TOKEN = "pit_mcp_Hx4Wq7Lc2Tn9Pb1Kd6Rs3Vy8Fm5Gz0Ju2Ne7Oa4Ui9Q";
const DOTS = "•".repeat(14);
const EXPOSED_TITLE =
  "Secrets stored before the master key was set must be considered exposed";
const GUIDE_LEAD = "Do this in a terminal on this computer";
const TOKENS_SENTENCE =
  "Regenerate both tokens: each old value stops working at once.";

const sealed: CredentialStorage = {
  credentialsSealed: true,
  plaintextTradingKeys: false,
  plaintextMarketDataKeys: false,
  sealedAt: SEALED_AT,
  resealedSecrets: 0,
};
const unsealed: CredentialStorage = {
  credentialsSealed: false,
  plaintextTradingKeys: true,
  plaintextMarketDataKeys: false,
  sealedAt: null,
  resealedSecrets: 0,
};
const signingOn: SigningKeysStatus = {
  keys: [
    {
      keyId: "old-key",
      fingerprint: "0000000000000000",
      createdAt: "2026-10-01T10:00:00Z",
      active: false,
    },
    {
      keyId: "active-key",
      fingerprint: FINGERPRINT,
      createdAt: "2026-10-15T14:10:00Z",
      active: true,
    },
  ],
  eSignEnabled: true,
};

// GET /access in `token` access and both tokens, `routes` added over them.
function tokenAccessRoutes(routes: Record<string, Route> = {}) {
  return {
    [`GET ${API}/access`]: () =>
      jsonResponse({ mode: "token", passwordSetAt: SEALED_AT, sessions: 1 }),
    [`GET ${API}/access/tokens/agent`]: () =>
      jsonResponse({ token: AGENT_TOKEN }),
    [`GET ${API}/access/tokens/api`]: () => jsonResponse({ token: API_TOKEN }),
    ...routes,
  };
}

function renderStorage({
  storage = sealed,
  signing = signingOn,
  access,
  routes = {},
}: {
  storage?: CredentialStorage;
  signing?: SigningKeysStatus;
  /** Renders the Access section beside it under this access mode. */
  access?: AccessMode;
  routes?: Record<string, Route>;
} = {}) {
  if (access !== undefined) {
    setPanelSession(access);
  }
  const fetchMock = serving(routes);
  renderWithApi(
    <MemoryRouter>
      <AccessTokensProvider>
        {access !== undefined && <AccessSection />}
        <StorageSection />
      </AccessTokensProvider>
    </MemoryRouter>,
    {
      api: {
        fetchCredentialStorage: vi.fn().mockResolvedValue(storage),
        fetchSigningKeys: vi.fn().mockResolvedValue(signing),
      },
      fetch: fetchMock as unknown as typeof fetch,
    },
  );
  return { fetchMock };
}

function row(name: string) {
  return screen.getByRole("group", { name });
}

// The exposed-secrets notice's body: its title and everything under it.
function notice() {
  const body = screen.getByText(EXPOSED_TITLE).parentElement;
  if (body === null) {
    throw new Error("the exposed-secrets title has no container");
  }
  return body;
}

// The token named `name` in the notice with the controls beside the name,
// once the access tokens have loaded.
async function noticeToken(name: string) {
  const pair = (await within(notice()).findByText(name)).parentElement;
  if (pair === null) {
    throw new Error(`the notice's ${name} has no container`);
  }
  return pair;
}

beforeEach(async () => {
  await i18n.changeLanguage("en");
});

describe("StorageSection", () => {
  it("shows sealed storage with the time sealing re-encrypted it", async () => {
    renderStorage();

    expect(
      screen.getByRole("heading", { name: "Storage, signing and accounts" }),
    ).toBeInTheDocument();
    const encryption = row("Encryption");
    expect(
      await within(encryption).findByText(
        `On · sealed ${formatDateTime(SEALED_AT)}`,
      ),
    ).toBeInTheDocument();
    expect(within(encryption).getByText("Master key")).toBeInTheDocument();
    expect(screen.queryByText(GUIDE_LEAD)).not.toBeInTheDocument();
  });

  it("shows sealed storage that sealing never re-encrypted as just on", async () => {
    renderStorage({ storage: { ...sealed, sealedAt: null } });

    const encryption = row("Encryption");
    expect(await within(encryption).findByText("On")).toBeInTheDocument();
    expect(within(encryption).queryByText(/sealed/)).not.toBeInTheDocument();
  });

  it("shows plain-text storage with the master key guide under it", async () => {
    renderStorage({ storage: unsealed });

    const encryption = row("Encryption");
    expect(
      await within(encryption).findByText("Off · stored keys are plain text"),
    ).toBeInTheDocument();
    expect(within(encryption).getByText("None")).toBeInTheDocument();
    expect(within(encryption).getByText(GUIDE_LEAD)).toBeInTheDocument();
    expect(
      within(encryption).getByRole("group", { name: "How Officer runs" }),
    ).toBeInTheDocument();
  });

  it.each([
    ["on with the active key", signingOn, `On · key ${FINGERPRINT}`],
    ["off", { ...signingOn, eSignEnabled: false }, "Off"],
    [
      "on without a key",
      { keys: [], eSignEnabled: true },
      "On · no signing key",
    ],
  ])(
    "shows decision signing %s and links to Signing keys",
    async (_, signing, value) => {
      renderStorage({ signing });

      const signingRow = row("Decision signing");
      expect(await within(signingRow).findByText(value)).toBeInTheDocument();
      expect(
        within(signingRow).getByRole("link", { name: "Signing Keys" }),
      ).toHaveAttribute("href", "/signing-keys");
    },
  );

  it.each([
    [{ accounts: false, account: "main" }, "One account · main"],
    [{ accounts: true, account: "" }, "Several accounts"],
  ])(
    "shows the account mode in force and links to its tab",
    async (features, value) => {
      setAccountScope(features);
      renderStorage();

      const accountMode = row("Account mode");
      expect(within(accountMode).getByText(value)).toBeInTheDocument();
      expect(
        within(accountMode).getByRole("link", { name: "Account mode tab" }),
      ).toHaveAttribute("href", "/service#account-mode");
      // The other rows settle their loads before the test ends.
      await within(row("Decision signing")).findByText(
        `On · key ${FINGERPRINT}`,
      );
      await within(row("Encryption")).findByText(
        `On · sealed ${formatDateTime(SEALED_AT)}`,
      );
    },
  );

  it("shows a failed storage load in its row", async () => {
    renderWithApi(
      <MemoryRouter>
        <AccessTokensProvider>
          <StorageSection />
        </AccessTokensProvider>
      </MemoryRouter>,
      {
        api: {
          fetchCredentialStorage: vi
            .fn()
            .mockRejectedValue(new Error("credential storage unavailable")),
          fetchSigningKeys: vi.fn().mockResolvedValue(signingOn),
        },
      },
    );

    expect(
      await within(row("Encryption")).findByRole("alert"),
    ).toHaveTextContent("credential storage unavailable");
  });

  it("shows no exposed-secrets notice when sealing re-encrypted nothing", async () => {
    renderStorage({ storage: { ...sealed, resealedSecrets: 0 } });

    await within(row("Encryption")).findByText(
      `On · sealed ${formatDateTime(SEALED_AT)}`,
    );
    expect(screen.queryByText(EXPOSED_TITLE)).not.toBeInTheDocument();
  });

  it("asks to replace exposed secrets and links to the connection keys", async () => {
    renderStorage({
      storage: { ...sealed, resealedSecrets: 3 },
      access: "open",
    });

    expect(await screen.findByText(EXPOSED_TITLE)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Sealing re-encrypted 3 stored secrets that had lain in the database file as plain text: anyone who copied the file or a backup before then can read them.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Trading connections" }),
    ).toHaveAttribute("href", "/trading-connections");
    expect(screen.getByRole("link", { name: "Market Data" })).toHaveAttribute(
      "href",
      "/market-data",
    );
    // Open access holds no tokens to regenerate.
    expect(
      within(notice()).queryByText(TOKENS_SENTENCE),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Regenerate" }),
    ).not.toBeInTheDocument();
  });

  it("asks to regenerate the tokens in token access even when they fail to load", async () => {
    renderStorage({
      storage: { ...sealed, resealedSecrets: 2 },
      access: "token",
      routes: tokenAccessRoutes({
        [`GET ${API}/access/tokens/api`]: () => errorResponse(500, "internal"),
      }),
    });

    expect(await screen.findByText(EXPOSED_TITLE)).toBeInTheDocument();
    expect(await screen.findByRole("alert")).toHaveTextContent("internal");
    expect(within(notice()).getByText(TOKENS_SENTENCE)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Regenerate" }),
    ).not.toBeInTheDocument();
  });

  it("regenerates a token from the notice through the Access section's action", async () => {
    const user = userEvent.setup();
    const { fetchMock } = renderStorage({
      storage: { ...sealed, resealedSecrets: 1 },
      access: "token",
      routes: tokenAccessRoutes({
        [`POST ${API}/access/tokens/agent/regenerate`]: () =>
          jsonResponse({ token: NEW_AGENT_TOKEN }),
      }),
    });

    expect(
      await screen.findByText(
        "Sealing re-encrypted 1 stored secret that had lain in the database file as plain text: anyone who copied the file or a backup before then can read it.",
      ),
    ).toBeInTheDocument();
    expect(within(notice()).getByText(TOKENS_SENTENCE)).toBeInTheDocument();
    // Each token's action reads as on its row, beside the token's name.
    expect(
      await within(await noticeToken("API token · REST")).findByRole("button", {
        name: "Regenerate",
      }),
    ).toBeInTheDocument();
    expect(
      within(notice()).getAllByRole("button", { name: "Regenerate" }),
    ).toHaveLength(2);
    expect(
      within(row("API token · REST")).getByRole("button", {
        name: "Regenerate",
      }),
    ).toBeInTheDocument();
    await user.click(
      within(await noticeToken("Agent token · MCP")).getByRole("button", {
        name: "Regenerate",
      }),
    );
    const dialog = screen.getByRole("alertdialog", {
      name: "Regenerate the agent token?",
    });
    await user.click(
      within(dialog).getByRole("button", { name: "Regenerate" }),
    );

    expect(
      await within(row("Agent token · MCP")).findByText(
        `pit_mcp_Hx4W${DOTS}Ui9Q`,
      ),
    ).toBeInTheDocument();
    expect(
      fetchMock.mock.calls.filter(
        ([url, init]) =>
          `${init.method} ${url}` ===
          `POST ${API}/access/tokens/agent/regenerate`,
      ),
    ).toHaveLength(1);
  });
});
