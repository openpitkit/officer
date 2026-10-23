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

import { act, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { renderWithApi as render } from "@/test/apiClient";
import { useState } from "react";
import { I18nextProvider } from "react-i18next";
import { MemoryRouter } from "react-router-dom";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

import type {
  CredentialStorage,
  SigningKey,
  SigningKeysStatus,
} from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { useSigningKeys } from "@/api/useSigningKeys";
import { SidebarProvider } from "@/components/SidebarContext";
import i18n from "@/i18n";
import { SigningKeys } from "@/pages/SigningKeys";
import { storage } from "@/test/credentialStorage";
import { mockReady } from "@/test/signingKeys";
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
// SensitiveDataBanner and the page itself poll GET /credential-storage.
vi.mock("@/api/useCredentialStorage");
const useCredentialStorageMock = vi.mocked(useCredentialStorage);
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

function mockLoading() {
  useSigningKeysMock.mockReturnValue({
    load: { state: "loading", data: null, error: null },
    reload: vi.fn(),
  } as ReturnType<typeof useSigningKeys>);
}

function mockStorage(load: LoadState<CredentialStorage>) {
  useCredentialStorageMock.mockReturnValue({ load, reload: vi.fn() });
}

function renderPage() {
  render(
    <I18nextProvider i18n={i18n}>
      <ThemeProvider storageKey="pit-test-theme" defaultMode="light">
        <DisplayPreferencesProvider
          densityStorageKey="pit-test-density"
          tradeStyleStorageKey="pit-test-style"
        >
          <MemoryRouter>
            <SidebarProvider>
              <SigningKeys />
            </SidebarProvider>
          </MemoryRouter>
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
  mockStorage(storage(true));
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

describe("SigningKeys page - empty state", () => {
  it("shows empty state when no keys are configured", () => {
    mockReady({ keys: [], eSignEnabled: false });
    renderPage();
    expect(screen.getByText(/No signing key configured/i)).toBeDefined();
  });
});

describe("SigningKeys page - active key card", () => {
  it("renders key id and fingerprint from loaded data", () => {
    mockReady(makeStatus());
    renderPage();
    expect(screen.getByText("test-key-id-1234")).toBeDefined();
    expect(screen.getByText("ab:cd:ef:01")).toBeDefined();
  });
});

describe("SigningKeys page - generate", () => {
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

describe("SigningKeys page - import", () => {
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

describe("SigningKeys page - master key", () => {
  const noticeTitle = "Approval signing requires a master key";

  it("explains and refuses key entry without a master key", async () => {
    mockReady(makeStatus());
    mockStorage(storage(false));
    renderPage();
    expect(screen.getByText(noticeTitle)).toBeInTheDocument();
    expect(
      screen.getByText(
        "Officer stores a signing key only sealed under a master key. This instance runs without one, so it cannot generate or import a key. Configure a master key and restart Officer to enable signing.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "How to set a master key" }),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Generate$/i })).toBeDisabled();

    await userEvent.click(
      screen.getByRole("button", { name: /Import your key/i }),
    );
    expect(
      screen.getByPlaceholderText(/Paste your private key here/i),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: /^Import$/i })).toBeDisabled();
  });

  it("ends the notice in the link to the master key guide on Service", () => {
    mockReady(makeStatus());
    mockStorage(storage(false));
    renderPage();
    const notice = screen.getByText(noticeTitle).parentElement as HTMLElement;
    expect(
      within(notice).getByRole("link", { name: "How to set a master key" }),
    ).toHaveAttribute("href", "/service#security");
  });

  it("leaves key entry open with a master key", async () => {
    mockReady(makeStatus());
    mockStorage(storage(true));
    renderPage();
    expect(screen.queryByText(noticeTitle)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Generate$/i })).toBeEnabled();

    await userEvent.click(
      screen.getByRole("button", { name: /Import your key/i }),
    );
    expect(
      screen.getByPlaceholderText(/Paste your private key here/i),
    ).toBeEnabled();
    expect(screen.getByRole("button", { name: /^Import$/i })).toBeEnabled();
  });

  it("refuses key entry while the master-key check runs", () => {
    mockReady(makeStatus());
    mockStorage({ state: "loading", data: null, error: null });
    renderPage();
    expect(screen.getByRole("status")).toHaveTextContent(
      "Checking whether stored keys are encrypted…",
    );
    expect(screen.getByRole("button", { name: /^Generate$/i })).toBeDisabled();
  });

  it("reports a failed master-key check and refuses key entry", async () => {
    mockReady(makeStatus());
    mockStorage({
      state: "error",
      data: null,
      error: "credential storage unavailable",
    });
    renderPage();
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not check whether stored keys are encrypted: credential storage unavailable",
    );
    expect(screen.queryByText(noticeTitle)).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /^Generate$/i })).toBeDisabled();

    await userEvent.click(
      screen.getByRole("button", { name: /Import your key/i }),
    );
    expect(screen.getByRole("button", { name: /^Import$/i })).toBeDisabled();
  });

  it("drops the generate or import hint from the empty key without a master key", () => {
    mockReady({ keys: [], eSignEnabled: false });
    mockStorage(storage(false));
    renderPage();
    expect(screen.getByText("No signing key configured.")).toBeInTheDocument();
    expect(
      screen.queryByText(/Generate or import one below/i),
    ).not.toBeInTheDocument();
  });

  it("keeps the generate or import hint on the empty key with a master key", () => {
    mockReady({ keys: [], eSignEnabled: false });
    mockStorage(storage(true));
    renderPage();
    expect(
      screen.getByText(
        "No signing key configured. Generate or import one below.",
      ),
    ).toBeInTheDocument();
  });

  it("disables the open generate confirmation once the master key is gone", async () => {
    mockReady(makeStatus());
    // The storage as polled by every reader: a later poll may unseal it.
    const readers = new Set<(next: LoadState<CredentialStorage>) => void>();
    useCredentialStorageMock.mockImplementation(
      function useLiveCredentialStorage() {
        const [load, setLoad] = useState(() => storage(true));
        readers.add(setLoad);
        return { load, reload: vi.fn() };
      },
    );
    renderPage();
    await userEvent.click(screen.getByRole("button", { name: /^Generate$/i }));
    const confirmBtn = within(screen.getByRole("alertdialog")).getByRole(
      "button",
      { name: /Generate$/i },
    );
    expect(confirmBtn).toBeEnabled();

    act(() => {
      for (const read of readers) read(storage(false));
    });
    expect(confirmBtn).toBeDisabled();
  });
});

describe("SigningKeys page - export", () => {
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

describe("SigningKeys page - eSign toggle", () => {
  const unsigned =
    "Envelopes go out unsigned: anyone who can write to the same channel can forge a decision. Turn the signature on before a program of yours executes orders on Officer's decisions.";

  it("warns that envelopes go out unsigned while the signature is off", () => {
    mockReady(makeStatus({ eSignEnabled: false }));
    renderPage();
    expect(screen.getByText(unsigned)).toBeInTheDocument();
  });

  it("drops the unsigned warning while the signature is on", () => {
    mockReady(makeStatus({ eSignEnabled: true }));
    renderPage();
    expect(screen.queryByText(unsigned)).not.toBeInTheDocument();
  });

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
      screen.getByText(/electronic signatures need an active signing key/i),
    ).toBeInTheDocument();
  });
});

describe("SigningKeys page - loading state", () => {
  it("renders without crashing while loading", () => {
    mockLoading();
    renderPage();
    // Cards are still rendered (page skeleton shows)
    expect(screen.getByText(/Signing Keys/i)).toBeDefined();
  });
});
