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
// Please see https://openpit.dev and the OWNERS file for details.

import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { I18nextProvider } from "react-i18next";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type { SigningKey, SigningKeysStatus } from "@/api/types";
import { useSigningKeys } from "@/api/useSigningKeys";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { SigningKeys } from "@/pages/SigningKeys";
import { DisplayPreferencesProvider } from "@/theme/DisplayPreferencesProvider";
import { ThemeProvider } from "@/theme/ThemeProvider";

// Radix Select requires pointer-capture stubs in jsdom.
beforeAll(() => {
  const proto = window.HTMLElement.prototype as unknown as Record<
    string,
    unknown
  >;
  proto.hasPointerCapture = () => false;
  proto.setPointerCapture = () => {};
  proto.releasePointerCapture = () => {};
  proto.scrollIntoView = () => {};
});

vi.mock("@/api/useSigningKeys");
// The page chrome renders PendingRestartBanner, which polls market data on
// mount. Stub it to a settled, no-restart state so its async update does not
// fire outside act() in tests that render synchronously.
vi.mock("@/api/useMarketData", () => ({
  useMarketData: () => ({
    load: { state: "ready", data: { restartRequired: false }, error: null },
    reload: () => {},
  }),
}));
const useSigningKeysMock = vi.mocked(useSigningKeys);
const exportPublicKeyMock = vi.fn();
const generateSigningKeyMock = vi.fn();
const importSigningKeyMock = vi.fn();
const setESignEnabledMock = vi.fn();

function makeKey(overrides: Partial<SigningKey> = {}): SigningKey {
  return {
    keyId: "test-key-id-1234",
    fingerprint: "ab:cd:ef:01",
    createdAt: "2026-01-01T00:00:00Z",
    active: true,
    ...overrides,
  };
}

function makeStatus(
  overrides: Partial<SigningKeysStatus> = {},
): SigningKeysStatus {
  return {
    keys: [makeKey()],
    eSignEnabled: true,
    ...overrides,
  };
}

function mockReady(status: SigningKeysStatus) {
  useSigningKeysMock.mockReturnValue({
    load: { state: "ready", data: status, error: null },
    reload: vi.fn(),
  } as ReturnType<typeof useSigningKeys>);
}

function mockLoading() {
  useSigningKeysMock.mockReturnValue({
    load: { state: "loading", data: null, error: null },
    reload: vi.fn(),
  } as ReturnType<typeof useSigningKeys>);
}

function renderPage() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
        >
          <SidebarProvider>
            <SigningKeys />
          </SidebarProvider>
        </DisplayPreferencesProvider>
      </ThemeProvider>
    </I18nextProvider>,
    {
      api: {
        exportPublicKey: exportPublicKeyMock,
        generateSigningKey: generateSigningKeyMock,
        importSigningKey: importSigningKeyMock,
        setESignEnabled: setESignEnabledMock,
      },
    },
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  generateSigningKeyMock.mockResolvedValue({
    key: makeKey(),
    publicKey: "-----BEGIN...",
  });
  importSigningKeyMock.mockResolvedValue({
    key: makeKey(),
    publicKey: "-----BEGIN...",
  });
  exportPublicKeyMock.mockResolvedValue(
    "-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----",
  );
  setESignEnabledMock.mockResolvedValue(undefined);
});

describe("SigningKeys page — empty state", () => {
  it("shows empty state when no keys are configured", () => {
    mockReady({ keys: [], eSignEnabled: false });
    renderPage();
    expect(screen.getByText(/No signing key configured/i)).toBeDefined();
  });
});

describe("SigningKeys page — active key card", () => {
  it("renders key id and fingerprint from loaded data", () => {
    mockReady(makeStatus());
    renderPage();
    expect(screen.getByText("test-key-id-1234")).toBeDefined();
    expect(screen.getByText("ab:cd:ef:01")).toBeDefined();
  });
});

describe("SigningKeys page — generate", () => {
  it("opens AlertDialog on Generate button click", async () => {
    mockReady(makeStatus());
    renderPage();
    const genBtn = screen.getByRole("button", { name: /^Generate$/i });
    await userEvent.click(genBtn);
    expect(screen.getByRole("alertdialog")).toBeDefined();
  });

  it("calls generateSigningKey on dialog confirm", async () => {
    mockReady(makeStatus());
    renderPage();
    await userEvent.click(screen.getByRole("button", { name: /^Generate$/i }));
    const dialog = screen.getByRole("alertdialog");
    const confirmBtn = within(dialog).getByRole("button", {
      name: /Generate$/i,
    });
    await userEvent.click(confirmBtn);
    await waitFor(() => expect(generateSigningKeyMock).toHaveBeenCalledOnce());
  });
});

describe("SigningKeys page — import", () => {
  it("shows validation error when importing empty key", async () => {
    mockReady(makeStatus());
    renderPage();
    // Switch to import mode
    await userEvent.click(
      screen.getByRole("button", { name: /Import your key/i }),
    );
    await userEvent.click(screen.getByRole("button", { name: /^Import$/i }));
    expect(
      screen.getByText(/Paste a private key before importing/i),
    ).toBeDefined();
    expect(importSigningKeyMock).not.toHaveBeenCalled();
  });

  it("calls importSigningKey with pasted key and chosen format", async () => {
    mockReady(makeStatus());
    renderPage();
    await userEvent.click(
      screen.getByRole("button", { name: /Import your key/i }),
    );
    const ta = screen.getByPlaceholderText(/Paste your private key here/i);
    await userEvent.type(
      ta,
      "-----BEGIN PRIVATE KEY-----\ntest\n-----END PRIVATE KEY-----",
    );
    await userEvent.click(screen.getByRole("button", { name: /^Import$/i }));
    await waitFor(() =>
      expect(importSigningKeyMock).toHaveBeenCalledWith(
        expect.stringContaining("BEGIN PRIVATE KEY"),
        "pem-pkcs8",
      ),
    );
  });
});

describe("SigningKeys page — export", () => {
  it("calls exportPublicKey with the selected format", async () => {
    mockReady(makeStatus());
    renderPage();
    // Click the export button (label key "currentKey.export.formatLabel")
    const exportBtn = screen.getByRole("button", { name: /Format/i });
    await userEvent.click(exportBtn);
    await waitFor(() =>
      expect(exportPublicKeyMock).toHaveBeenCalledWith("pem-pkcs8"),
    );
  });

  it("shows the exported public key in a copyable snippet", async () => {
    mockReady(makeStatus());
    renderPage();
    await userEvent.click(screen.getByRole("button", { name: /Format/i }));
    await waitFor(() =>
      expect(screen.getByDisplayValue(/BEGIN PUBLIC KEY/)).toBeDefined(),
    );
  });
});

describe("SigningKeys page — eSign toggle", () => {
  it("calls setESignEnabled(false) when toggled off", async () => {
    mockReady(makeStatus({ eSignEnabled: true }));
    renderPage();
    const checkbox = screen.getByRole("checkbox");
    await userEvent.click(checkbox);
    await waitFor(() =>
      expect(setESignEnabledMock).toHaveBeenCalledWith(false),
    );
  });

  it("calls setESignEnabled(true) when toggled on", async () => {
    mockReady(makeStatus({ eSignEnabled: false }));
    renderPage();
    const checkbox = screen.getByRole("checkbox");
    await userEvent.click(checkbox);
    await waitFor(() => expect(setESignEnabledMock).toHaveBeenCalledWith(true));
  });

  it("does not enable eSign without an active signing key", async () => {
    mockReady({ keys: [], eSignEnabled: false });
    renderPage();
    const checkbox = screen.getByRole("checkbox");
    await userEvent.click(checkbox);

    expect(setESignEnabledMock).not.toHaveBeenCalled();
    expect(checkbox).not.toBeChecked();
    expect(
      screen.getByText(/generate or import a signing key/i),
    ).toBeInTheDocument();
  });
});

describe("SigningKeys page — loading state", () => {
  it("renders without crashing while loading", () => {
    mockLoading();
    renderPage();
    // Cards are still rendered (page skeleton shows)
    expect(screen.getByText(/Signing Keys/i)).toBeDefined();
  });
});
