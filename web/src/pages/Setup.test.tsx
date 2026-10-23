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
import userEvent, { type UserEvent } from "@testing-library/user-event";
import { useState, type ReactElement } from "react";
import { MemoryRouter, Route as RouterRoute, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { AccessMode } from "@/api/access";
import { useAccountMode, type AccountModeState } from "@/api/accountMode";
import {
  completeSetup,
  declineEncryption,
  fetchSetup,
  putSetupProgress,
  type SetupState,
} from "@/api/setup";
import type { CredentialStorage } from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { ApiError } from "@/framework";
import i18n from "@/i18n";
import { checkSession, setPanelSession } from "@/lib/panelSession";
import { Setup, SetupReview } from "@/pages/Setup";
import {
  errorResponse,
  jsonResponse,
  renderWithApi,
  serving,
  type Route,
} from "@/test/apiClient";
import { storage } from "@/test/credentialStorage";
import { deferred } from "@/test/deferred";
import { mockReady } from "@/test/signingKeys";
import { ThemeProvider } from "@/theme/ThemeProvider";

const { admitMock } = vi.hoisted(() => ({
  admitMock: vi.fn<(accessMode: AccessMode) => void>(),
}));

vi.mock("@/api/setup", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/setup")>()),
  fetchSetup: vi.fn(),
  putSetupProgress: vi.fn(),
  declineEncryption: vi.fn(),
  completeSetup: vi.fn(),
}));
vi.mock("@/components/StartupGate", () => ({
  useStartupAdmit: () => admitMock,
}));
vi.mock("@/lib/panelSession", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/panelSession")>()),
  checkSession: vi.fn(),
}));
vi.mock("@/api/accountMode", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/api/accountMode")>()),
  useAccountMode: vi.fn(),
}));
vi.mock("@/api/useSigningKeys");
vi.mock("@/api/useCredentialStorage");

const API = "/app/api/v1";
const AGENT_TOKEN = "pit_agent_0123456789abcdef";
const PASSWORD = "a-new-panel-password";
const REFUSAL = "password must be at least 15 characters";
const FINGERPRINT = "ab:cd:ef:01";
const ACKNOWLEDGE_NONE = "I understand: my keys are stored unencrypted.";
const ENCRYPTION_ON = "Encryption is on. Officer sealed its database.";
const NONE_WARNING =
  "Without encryption, whoever gets the database file gets your keys";
const MASTER_KEY_GUIDE = "Do this in a terminal on this computer";
const EXPOSED_SECRETS =
  "Secrets stored before the master key was set must be considered exposed";

const TITLES = {
  welcome: "Set up Pit Officer",
  accounts: "How many accounts will you use?",
  encryption: "Seal stored keys with a master key?",
  signing: "Sign Officer's decisions?",
  access: "Who may use Officer?",
  agent: "Connect your AI agent",
  done: "Officer is set up",
};

function accountMode(
  overrides: Partial<AccountModeState> = {},
): AccountModeState {
  return {
    mode: "multi",
    account: "",
    features: { accounts: true, account: "" },
    restartRequired: false,
    ...overrides,
  };
}

function mockAccountMode(state: AccountModeState) {
  vi.mocked(useAccountMode).mockReturnValue({
    load: { state: "ready", data: state, error: null },
    reload: vi.fn(),
  });
}

function mockStorage(sealed: boolean, resealedSecrets = 0) {
  vi.mocked(useCredentialStorage).mockReturnValue({
    load: storage(sealed, { resealedSecrets }),
    reload: vi.fn(),
  });
}

/** The storage as polled by every reader; the returned call sets a new answer. */
function liveStorage(initial: LoadState<CredentialStorage>) {
  const readers = new Set<(next: LoadState<CredentialStorage>) => void>();
  vi.mocked(useCredentialStorage).mockImplementation(
    function useLiveCredentialStorage() {
      const [load, setLoad] = useState(initial);
      readers.add(setLoad);
      return { load, reload: vi.fn() };
    },
  );
  return (next: LoadState<CredentialStorage>) =>
    act(() => {
      for (const read of readers) read(next);
    });
}

/** A deferred write whose settling the wizard sees within act. */
function held() {
  const { promise, resolve, reject } = deferred<void>();
  return {
    promise,
    resolve: () => act(async () => resolve()),
    reject: (reason: unknown) => act(async () => reject(reason)),
  };
}

function renderSetup(
  state: Partial<SetupState> = {},
  routes: Record<string, Route> = {},
) {
  vi.mocked(fetchSetup).mockResolvedValue({
    step: "welcome",
    encryptionDeclinedAt: null,
    ...state,
  });
  return renderAtSetup(<Setup />, routes);
}

/**
 * Renders the review under `accessMode`, the mode of this browser's session,
 * at the location `entry`.
 */
function renderReview(
  accessMode: AccessMode,
  routes: Record<string, Route> = {},
  entry = "/setup",
) {
  setPanelSession(accessMode);
  return renderAtSetup(<SetupReview accessMode={accessMode} />, routes, entry);
}

function renderAtSetup(
  wizard: ReactElement,
  routes: Record<string, Route>,
  entry = "/setup",
) {
  const restartService = vi
    .fn<() => Promise<void>>()
    .mockResolvedValue(undefined);
  const fetchMock = serving({
    [`GET ${API}/access/tokens/agent`]: () =>
      jsonResponse({ token: AGENT_TOKEN }),
    ...routes,
  });
  renderWithApi(
    <ThemeProvider storageKey="pit-officer-test-theme" defaultMode="light">
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <RouterRoute path="/setup" element={wizard} />
          <RouterRoute path="/" element={<p>Home</p>} />
        </Routes>
      </MemoryRouter>
    </ThemeProvider>,
    {
      fetch: fetchMock as unknown as typeof fetch,
      api: {
        restartService,
        fetchAccounts: vi.fn().mockResolvedValue([]),
        setESignEnabled: vi.fn().mockResolvedValue(undefined),
      },
    },
  );
  return { restartService };
}

function heading(name: string) {
  return screen.findByRole("heading", { level: 1, name });
}

function railStep(name: string, rail = "First start") {
  return within(screen.getByRole("navigation", { name: rail })).getByRole(
    "button",
    { name: new RegExp(`^${name}`) },
  );
}

function next(name = "Continue") {
  return screen.getByRole("button", { name });
}

async function enterPassword(user: UserEvent, repeat = PASSWORD) {
  await user.type(screen.getByLabelText("New panel password"), PASSWORD);
  await user.type(screen.getByLabelText("Repeat the new password"), repeat);
}

async function finishWithToken(user: UserEvent) {
  await heading(TITLES.access);
  await enterPassword(user);
  await user.click(next("Finish setup"));
  await heading(TITLES.agent);
}

/** Saves several accounts on the accounts step and confirms the save. */
async function saveSeveralAccounts(user: UserEvent) {
  await heading(TITLES.accounts);
  await user.click(screen.getByRole("button", { name: "Several accounts" }));
  await user.click(screen.getByRole("button", { name: "Save" }));
  await user.click(
    within(await screen.findByRole("alertdialog")).getByRole("button", {
      name: "Save",
    }),
  );
}

/** Picks no encryption on the encryption step and acknowledges it. */
async function declineOnEncryption(user: UserEvent) {
  await heading(TITLES.encryption);
  await user.click(screen.getByRole("radio", { name: "Without encryption" }));
  await user.click(screen.getByRole("checkbox", { name: ACKNOWLEDGE_NONE }));
}

beforeEach(async () => {
  vi.clearAllMocks();
  await i18n.changeLanguage("en");
  vi.mocked(fetchSetup).mockReset();
  vi.mocked(putSetupProgress).mockReset().mockResolvedValue(undefined);
  vi.mocked(declineEncryption).mockReset().mockResolvedValue(undefined);
  vi.mocked(completeSetup).mockReset().mockResolvedValue(undefined);
  admitMock.mockImplementation((accessMode) => setPanelSession(accessMode));
  mockAccountMode(accountMode());
  mockStorage(true);
  mockReady({
    keys: [
      {
        keyId: "test-key-id-1234",
        fingerprint: FINGERPRINT,
        createdAt: "2026-01-01T00:00:00Z",
        active: true,
      },
    ],
    eSignEnabled: true,
  });
});

describe("Setup", () => {
  it("opens at the stored step with the earlier steps passed", async () => {
    const user = userEvent.setup();
    renderSetup({ step: "signing" });

    await heading(TITLES.signing);
    expect(screen.getByText("Step 4 of 7")).toBeInTheDocument();
    for (const name of ["Welcome", "Accounts", "Encryption"]) {
      expect(railStep(name)).toBeEnabled();
    }
    expect(railStep("Signing")).toHaveAttribute("aria-current", "step");
    for (const name of ["Access", "Your agent", "Done"]) {
      expect(railStep(name)).toBeDisabled();
    }
    expect(railStep("Accounts")).toHaveTextContent("Several accounts");
    expect(railStep("Encryption")).toHaveTextContent("Master key");

    await user.click(railStep("Encryption"));

    await heading(TITLES.encryption);
    expect(putSetupProgress).toHaveBeenCalledWith(
      expect.anything(),
      "encryption",
    );
    expect(railStep("Signing")).toBeDisabled();
  });

  it("retries a failed load", async () => {
    const user = userEvent.setup();
    vi.mocked(fetchSetup).mockRejectedValueOnce(
      new ApiError("database is locked", "internal", 500),
    );
    renderSetup();

    expect(await screen.findByText("database is locked")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Retry" }));

    await heading(TITLES.welcome);
    expect(fetchSetup).toHaveBeenCalledTimes(2);
  });

  it("stores the previous step on Back", async () => {
    const user = userEvent.setup();
    renderSetup({ step: "encryption" });

    await heading(TITLES.encryption);
    await user.click(screen.getByRole("button", { name: "Back" }));

    await heading(TITLES.accounts);
    expect(putSetupProgress).toHaveBeenCalledTimes(1);
    expect(putSetupProgress).toHaveBeenCalledWith(
      expect.anything(),
      "accounts",
    );
  });

  it("disables the footer and the rail while a move runs", async () => {
    const user = userEvent.setup();
    const write = held();
    vi.mocked(putSetupProgress).mockReturnValueOnce(write.promise);
    renderSetup({ step: "signing" });

    await heading(TITLES.signing);
    await user.click(next());

    expect(next()).toBeDisabled();
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    for (const name of ["Welcome", "Accounts", "Encryption", "Signing"]) {
      expect(railStep(name)).toBeDisabled();
    }

    await write.resolve();
    await heading(TITLES.access);
    expect(railStep("Signing")).toBeEnabled();
  });

  it("starts no second move while one runs", async () => {
    const user = userEvent.setup();
    mockStorage(false);
    const write = held();
    vi.mocked(putSetupProgress).mockReturnValueOnce(write.promise);
    renderSetup({ step: "signing" });

    await heading(TITLES.signing);
    const masterKey = screen.getByRole("button", { name: "Set a master key" });
    await user.click(masterKey);
    await user.click(masterKey);

    expect(putSetupProgress).toHaveBeenCalledTimes(1);
    await write.resolve();
    await heading(TITLES.encryption);
    expect(putSetupProgress).toHaveBeenCalledTimes(1);
  });

  describe("welcome", () => {
    it("stores the accounts step on Start setup and shows it", async () => {
      const user = userEvent.setup();
      renderSetup();

      await heading(TITLES.welcome);
      expect(
        screen.getByText("Officer is closed until setup is finished"),
      ).toBeInTheDocument();
      await user.click(next("Start setup"));

      await heading(TITLES.accounts);
      expect(putSetupProgress).toHaveBeenCalledTimes(1);
      expect(putSetupProgress).toHaveBeenCalledWith(
        expect.anything(),
        "accounts",
      );
    });

    it("stays when the progress write fails and shows the error", async () => {
      const user = userEvent.setup();
      vi.mocked(putSetupProgress).mockRejectedValue(
        new ApiError("database is locked", "internal", 500),
      );
      renderSetup();

      await heading(TITLES.welcome);
      await user.click(next("Start setup"));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "database is locked",
      );
      expect(
        screen.getByRole("heading", { level: 1, name: TITLES.welcome }),
      ).toBeInTheDocument();
    });
  });

  describe("accounts", () => {
    it.each([
      { case: "no mode is stored", state: accountMode({ mode: null }) },
      {
        case: "the mode needs a restart",
        state: accountMode({ restartRequired: true }),
      },
    ])("keeps Continue disabled while $case", async ({ state }) => {
      mockAccountMode(state);
      renderSetup({ step: "accounts" });

      await heading(TITLES.accounts);
      expect(next()).toBeDisabled();
      expect(
        screen.getByText(
          "Save the mode to continue. Officer restarts to apply it.",
        ),
      ).toBeInTheDocument();
    });

    it("enables Continue once the poll reloaded after a save", async () => {
      const user = userEvent.setup();
      // The reload's answer, the stored mode, arrives when the test sends it.
      const reload = vi.fn();
      let answer!: () => void;
      vi.mocked(useAccountMode).mockImplementation(
        function useStoredAccountMode() {
          const [load, setLoad] = useState<LoadState<AccountModeState>>({
            state: "ready",
            data: accountMode({ mode: null }),
            error: null,
          });
          answer = () =>
            act(() =>
              setLoad({ state: "ready", data: accountMode(), error: null }),
            );
          return { load, reload };
        },
      );
      renderSetup(
        { step: "accounts" },
        {
          [`PUT ${API}/account-mode`]: () => jsonResponse(accountMode()),
        },
      );

      await heading(TITLES.accounts);
      expect(next()).toBeDisabled();
      await saveSeveralAccounts(user);

      await waitFor(() => expect(reload).toHaveBeenCalledTimes(1));
      expect(next()).toBeDisabled();
      answer();
      expect(next()).toBeEnabled();
      expect(
        screen.getByText("Change later: Service › Account mode"),
      ).toBeInTheDocument();
    });

    it("starts no move while Officer restarts", async () => {
      const user = userEvent.setup();
      mockAccountMode(accountMode({ mode: null }));
      const { restartService } = renderSetup(
        { step: "accounts" },
        {
          [`PUT ${API}/account-mode`]: () =>
            jsonResponse(accountMode({ restartRequired: true })),
        },
      );

      await saveSeveralAccounts(user);
      await waitFor(() => expect(restartService).toHaveBeenCalledTimes(1));
      await waitFor(() =>
        expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
      );

      expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
      for (const name of ["Welcome", "Accounts"]) {
        expect(railStep(name)).toBeDisabled();
      }
      await user.click(railStep("Welcome"));
      // Only the position stored before the restart.
      expect(putSetupProgress).toHaveBeenCalledTimes(1);
      expect(putSetupProgress).toHaveBeenCalledWith(
        expect.anything(),
        "encryption",
      );
    });

    it("lets the moves go again when the restart fails", async () => {
      mockAccountMode(accountMode({ mode: null }));
      const user = userEvent.setup();
      const { restartService } = renderSetup(
        { step: "accounts" },
        {
          [`PUT ${API}/account-mode`]: () =>
            jsonResponse(accountMode({ restartRequired: true })),
        },
      );
      restartService.mockRejectedValueOnce(
        new ApiError("restart is refused", "internal", 500),
      );

      await saveSeveralAccounts(user);

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "restart is refused",
      );
      expect(screen.getByRole("button", { name: "Back" })).toBeEnabled();
      for (const name of ["Welcome", "Accounts"]) {
        expect(railStep(name)).toBeEnabled();
      }
      await user.click(screen.getByRole("button", { name: "Back" }));

      await heading(TITLES.welcome);
      expect(putSetupProgress).toHaveBeenCalledWith(
        expect.anything(),
        "welcome",
      );
    });

    it("stores the encryption step before Officer restarts", async () => {
      const user = userEvent.setup();
      mockAccountMode(accountMode({ mode: null }));
      const write = held();
      vi.mocked(putSetupProgress).mockReturnValueOnce(write.promise);
      const { restartService } = renderSetup(
        { step: "accounts" },
        {
          [`PUT ${API}/account-mode`]: () =>
            jsonResponse(accountMode({ restartRequired: true })),
        },
      );

      await saveSeveralAccounts(user);

      await waitFor(() =>
        expect(putSetupProgress).toHaveBeenCalledWith(
          expect.anything(),
          "encryption",
        ),
      );
      expect(restartService).not.toHaveBeenCalled();
      await write.resolve();
      await waitFor(() => expect(restartService).toHaveBeenCalledTimes(1));
      expect(putSetupProgress).toHaveBeenCalledTimes(1);
      expect(next()).toBeDisabled();
    });

    it("does not restart when storing the encryption step fails", async () => {
      const user = userEvent.setup();
      mockAccountMode(accountMode({ mode: null }));
      const write = held();
      vi.mocked(putSetupProgress).mockReturnValueOnce(write.promise);
      const { restartService } = renderSetup(
        { step: "accounts" },
        {
          [`PUT ${API}/account-mode`]: () =>
            jsonResponse(accountMode({ restartRequired: true })),
        },
      );

      await saveSeveralAccounts(user);
      await waitFor(() => expect(putSetupProgress).toHaveBeenCalledTimes(1));
      await write.reject(new ApiError("database is locked", "internal", 500));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "database is locked",
      );
      expect(restartService).not.toHaveBeenCalled();
    });
  });

  describe("encryption", () => {
    it("keeps Continue disabled for a master key until the storage is sealed", async () => {
      const answer = liveStorage(storage(false));
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "With a master key" }),
      ).toBeChecked();
      const region = screen
        .getAllByRole("status")
        .find((status) =>
          status.textContent?.startsWith(
            "Waiting for Officer to come back with the key",
          ),
        );
      expect(region).toBeDefined();
      expect(next()).toBeDisabled();

      answer(storage(true));

      // The same live region announces the sealing.
      expect(region).toBeInTheDocument();
      expect(region).toHaveTextContent(ENCRYPTION_ON);
      expect(next()).toBeEnabled();
    });

    it("shows the master key guide only until the database is sealed", async () => {
      const answer = liveStorage(storage(false));
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      expect(screen.getByText(MASTER_KEY_GUIDE)).toBeInTheDocument();

      answer(storage(true));

      // Its commands would overwrite the key in force.
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(screen.queryByText(MASTER_KEY_GUIDE)).not.toBeInTheDocument();
    });

    it("leaves the warning of exposed secrets to Service › Security", async () => {
      mockStorage(true, 2);
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(screen.queryByText(EXPOSED_SECRETS)).not.toBeInTheDocument();
    });

    it("declines encryption once acknowledged, then stores the signing step", async () => {
      const user = userEvent.setup();
      mockStorage(false);
      const decline = held();
      vi.mocked(declineEncryption).mockReturnValueOnce(decline.promise);
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      await user.click(
        screen.getByRole("radio", { name: "Without encryption" }),
      );
      expect(next()).toBeDisabled();
      await user.click(
        screen.getByRole("checkbox", { name: ACKNOWLEDGE_NONE }),
      );
      expect(next()).toBeEnabled();
      await user.click(next());

      expect(declineEncryption).toHaveBeenCalledTimes(1);
      expect(putSetupProgress).not.toHaveBeenCalled();
      await decline.resolve();

      await heading(TITLES.signing);
      expect(putSetupProgress).toHaveBeenCalledTimes(1);
      expect(putSetupProgress).toHaveBeenCalledWith(
        expect.anything(),
        "signing",
      );
    });

    it("stays at the encryption step when the decline fails", async () => {
      const user = userEvent.setup();
      mockStorage(false);
      const decline = held();
      vi.mocked(declineEncryption).mockReturnValueOnce(decline.promise);
      renderSetup({ step: "encryption" });

      await declineOnEncryption(user);
      await user.click(next());
      await decline.reject(
        new ApiError("credentials are sealed", "conflict", 409),
      );

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "credentials are sealed",
      );
      expect(
        screen.getByRole("heading", { level: 1, name: TITLES.encryption }),
      ).toBeInTheDocument();
      expect(putSetupProgress).not.toHaveBeenCalled();
    });

    it("opens a recorded decline on no encryption, acknowledged", async () => {
      mockStorage(false);
      renderSetup({
        step: "encryption",
        encryptionDeclinedAt: "2026-10-15T09:00:00Z",
      });

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeChecked();
      expect(
        screen.getByRole("checkbox", { name: ACKNOWLEDGE_NONE }),
      ).toBeChecked();
      expect(next()).toBeEnabled();
    });

    it("opens a sealed database on the master key, over a recorded decline", async () => {
      renderSetup({
        step: "encryption",
        encryptionDeclinedAt: "2026-10-15T09:00:00Z",
      });

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "With a master key" }),
      ).toBeChecked();
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(next()).toBeEnabled();
    });

    it("keeps a recorded decline from going on until the storage answers", async () => {
      const answer = liveStorage({ state: "loading", data: null, error: null });
      renderSetup({
        step: "encryption",
        encryptionDeclinedAt: "2026-10-15T09:00:00Z",
      });

      await heading(TITLES.encryption);
      expect(screen.getByRole("status", { name: "Loading" })).toBeVisible();
      expect(next()).toBeDisabled();

      answer(storage(false));

      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeChecked();
      expect(
        screen.queryByRole("status", { name: "Loading" }),
      ).not.toBeInTheDocument();
      expect(next()).toBeEnabled();
    });

    it("shows a failed storage load under a recorded decline", async () => {
      vi.mocked(useCredentialStorage).mockReturnValue({
        load: { state: "error", data: null, error: "connection refused" },
        reload: vi.fn(),
      });
      renderSetup({
        step: "encryption",
        encryptionDeclinedAt: "2026-10-15T09:00:00Z",
      });

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeChecked();
      expect(screen.getByRole("alert")).toHaveTextContent("connection refused");
      expect(next()).toBeDisabled();
    });

    it("turns a decline into the master key once the database is sealed", async () => {
      const user = userEvent.setup();
      const answer = liveStorage(storage(false));
      renderSetup({ step: "encryption" });

      await declineOnEncryption(user);
      await user.click(next());
      await heading(TITLES.signing);
      answer(storage(true));
      await user.click(screen.getByRole("button", { name: "Back" }));

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "With a master key" }),
      ).toBeChecked();
      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeDisabled();
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      await user.click(next());

      await heading(TITLES.signing);
      expect(declineEncryption).toHaveBeenCalledTimes(1);
      expect(putSetupProgress).toHaveBeenLastCalledWith(
        expect.anything(),
        "signing",
      );
    });

    it("keeps the master key over a sealed database", async () => {
      const user = userEvent.setup();
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      const none = screen.getByRole("radio", { name: "Without encryption" });
      expect(none).toBeDisabled();
      await user.click(none);

      expect(
        screen.getByRole("radio", { name: "With a master key" }),
      ).toBeChecked();
      expect(screen.queryByText(NONE_WARNING)).not.toBeInTheDocument();
    });

    it("marks the storage stale when a refresh fails", async () => {
      vi.mocked(useCredentialStorage).mockReturnValue({
        load: storage(false, { error: "connection refused" }),
        reload: vi.fn(),
      });
      renderSetup({ step: "encryption" });

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("status", { name: "Stale data: connection refused" }),
      ).toBeInTheDocument();
    });
  });

  it("returns to the encryption step from the signing master-key action", async () => {
    const user = userEvent.setup();
    mockStorage(false);
    renderSetup({ step: "signing" });

    await heading(TITLES.signing);
    await user.click(screen.getByRole("button", { name: "Set a master key" }));

    await heading(TITLES.encryption);
    expect(putSetupProgress).toHaveBeenCalledWith(
      expect.anything(),
      "encryption",
    );
  });

  describe("access", () => {
    it("keeps Finish setup disabled while the repeat differs", async () => {
      const user = userEvent.setup();
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await enterPassword(user, `${PASSWORD}-typo`);

      expect(next("Finish setup")).toBeDisabled();
      expect(completeSetup).not.toHaveBeenCalled();
    });

    it("finishes in token access and shows the agent step with the warning", async () => {
      const user = userEvent.setup();
      renderSetup({ step: "access" });

      await finishWithToken(user);

      expect(completeSetup).toHaveBeenCalledWith(expect.anything(), {
        access: "token",
        password: PASSWORD,
      });
      expect(admitMock).toHaveBeenCalledWith("token");
      expect(
        screen.getByText(
          "Give the agent this token only - never the API token or the panel password: with those it steps around its MCP limits.",
        ),
      ).toBeInTheDocument();
      expect(
        screen.getByText(
          "Setup finished. REST and MCP answer by the access you chose.",
        ),
      ).toBeInTheDocument();
    });

    it("finishes in open access only once acknowledged", async () => {
      const user = userEvent.setup();
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await user.click(screen.getByRole("radio", { name: "No token" }));
      expect(next("Finish setup")).toBeDisabled();
      await user.click(
        screen.getByRole("checkbox", {
          name: "I understand: any software on this computer can control Officer.",
        }),
      );
      await user.click(next("Finish setup"));

      await heading(TITLES.agent);
      expect(completeSetup).toHaveBeenCalledWith(expect.anything(), {
        access: "open",
        acknowledged: true,
      });
      expect(admitMock).toHaveBeenCalledWith("open");
      expect(
        screen.queryByText(/Give the agent this token only/),
      ).not.toBeInTheDocument();
    });

    it("shows a refused password under the field", async () => {
      const user = userEvent.setup();
      vi.mocked(completeSetup).mockRejectedValue(
        new ApiError(REFUSAL, "validation", 422),
      );
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await enterPassword(user);
      await user.click(next("Finish setup"));

      const password = await screen.findByLabelText("New panel password");
      await waitFor(() =>
        expect(password).toHaveAccessibleDescription(REFUSAL),
      );
      expect(password).toHaveValue("");
      expect(password).toHaveFocus();
      expect(next("Finish setup")).toBeDisabled();
      expect(admitMock).not.toHaveBeenCalled();
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    });

    it("drops a refused password when the choice changes", async () => {
      const user = userEvent.setup();
      vi.mocked(completeSetup).mockRejectedValueOnce(
        new ApiError(REFUSAL, "validation", 422),
      );
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await enterPassword(user);
      await user.click(next("Finish setup"));
      await waitFor(() =>
        expect(
          screen.getByLabelText("New panel password"),
        ).toHaveAccessibleDescription(REFUSAL),
      );

      await user.click(screen.getByRole("radio", { name: "No token" }));
      await user.click(
        screen.getByRole("radio", { name: "Token and password" }),
      );

      expect(screen.getByLabelText("New panel password")).toHaveValue("");
      expect(screen.queryByText(REFUSAL)).not.toBeInTheDocument();
    });

    it("shows a refused completion in the banner and does not admit", async () => {
      const user = userEvent.setup();
      vi.mocked(completeSetup).mockRejectedValue(
        new ApiError("account mode is not chosen", "conflict", 409),
      );
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await enterPassword(user);
      await user.click(next("Finish setup"));

      expect(await screen.findByRole("alert")).toHaveTextContent(
        "account mode is not chosen",
      );
      expect(admitMock).not.toHaveBeenCalled();
      expect(
        screen.getByRole("heading", { level: 1, name: TITLES.access }),
      ).toBeInTheDocument();
      expect(
        screen.getByLabelText("New panel password"),
      ).not.toHaveAccessibleDescription("account mode is not chosen");
    });

    it("drops the password and its refusal when the step is left", async () => {
      const user = userEvent.setup();
      vi.mocked(completeSetup).mockRejectedValueOnce(
        new ApiError(REFUSAL, "validation", 422),
      );
      renderSetup({ step: "access" });

      await heading(TITLES.access);
      await enterPassword(user);
      await user.click(next("Finish setup"));
      await waitFor(() =>
        expect(
          screen.getByLabelText("New panel password"),
        ).toHaveAccessibleDescription(REFUSAL),
      );
      await enterPassword(user);
      expect(next("Finish setup")).toBeEnabled();

      await user.click(screen.getByRole("button", { name: "Back" }));
      await heading(TITLES.signing);
      await user.click(next());
      await heading(TITLES.access);

      expect(screen.getByLabelText("New panel password")).toHaveValue("");
      expect(next("Finish setup")).toBeDisabled();
      expect(screen.queryByText(REFUSAL)).not.toBeInTheDocument();
    });
  });

  it.each([
    {
      call: "the load",
      state: {},
      arrange: () =>
        vi
          .mocked(fetchSetup)
          .mockRejectedValueOnce(
            new ApiError("setup complete", "setup_complete", 409),
          ),
      trigger: null,
    },
    {
      call: "a progress write",
      state: {},
      arrange: () =>
        vi
          .mocked(putSetupProgress)
          .mockRejectedValueOnce(
            new ApiError("setup complete", "setup_complete", 409),
          ),
      trigger: async (user: UserEvent) => {
        await heading(TITLES.welcome);
        await user.click(next("Start setup"));
      },
    },
    {
      call: "the progress write before a restart",
      state: { step: "accounts" },
      arrange: () => {
        mockAccountMode(accountMode({ mode: null }));
        vi.mocked(putSetupProgress).mockRejectedValueOnce(
          new ApiError("setup complete", "setup_complete", 409),
        );
      },
      trigger: saveSeveralAccounts,
    },
    {
      call: "the decline",
      state: { step: "encryption" },
      arrange: () => {
        mockStorage(false);
        vi.mocked(declineEncryption).mockRejectedValueOnce(
          new ApiError("setup complete", "setup_complete", 409),
        );
      },
      trigger: async (user: UserEvent) => {
        await declineOnEncryption(user);
        await user.click(next());
      },
    },
    {
      call: "the completion",
      state: { step: "access" },
      arrange: () =>
        vi
          .mocked(completeSetup)
          .mockRejectedValueOnce(
            new ApiError("setup complete", "setup_complete", 409),
          ),
      trigger: async (user: UserEvent) => {
        await heading(TITLES.access);
        await enterPassword(user);
        await user.click(next("Finish setup"));
      },
    },
  ] satisfies {
    call: string;
    state: Partial<SetupState>;
    arrange: () => void;
    trigger: ((user: UserEvent) => Promise<void>) | null;
  }[])(
    "hands over to the startup gate when $call finds setup complete",
    async ({ state, arrange, trigger }) => {
      const user = userEvent.setup();
      // A queued answer goes before the default renderSetup sets.
      arrange();
      const { restartService } = renderSetup(state, {
        [`PUT ${API}/account-mode`]: () =>
          jsonResponse(accountMode({ restartRequired: true })),
      });
      if (trigger !== null) {
        await trigger(user);
      }

      await waitFor(() => expect(checkSession).toHaveBeenCalledTimes(1));
      expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
      expect(screen.queryByRole("heading")).not.toBeInTheDocument();
      expect(restartService).not.toHaveBeenCalled();
      expect(admitMock).not.toHaveBeenCalled();
    },
  );

  it("goes from the agent step to the summary and opens the panel", async () => {
    const user = userEvent.setup();
    renderSetup({ step: "access" });

    await finishWithToken(user);
    for (const name of ["Welcome", "Accounts", "Encryption", "Signing"]) {
      expect(railStep(name)).toBeDisabled();
    }
    expect(
      screen.queryByRole("button", { name: "Back" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Connect later" }));

    await heading(TITLES.done);
    expect(screen.getByText("Step 7 of 7")).toBeInTheDocument();
    for (const name of [
      "Welcome",
      "Accounts",
      "Encryption",
      "Signing",
      "Access",
      "Your agent",
      "Done",
    ]) {
      expect(railStep(name)).toBeDisabled();
    }
    expect(
      screen.getByRole("row", {
        name: "Accounts Several accounts Service › Account mode",
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("row", {
        name: `Signing On · key ${FINGERPRINT} Signing Keys`,
      }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("row", {
        name: "Access Token and password Service › Security",
      }),
    ).toBeInTheDocument();
    await user.click(next("Open the panel"));

    expect(await screen.findByText("Home")).toBeInTheDocument();
  });

  it("renders in Russian", async () => {
    await i18n.changeLanguage("ru");
    renderSetup();

    expect(await heading("Настройка Pit Officer")).toBeInTheDocument();
  });
});

describe("SetupReview", () => {
  const GUIDE = "First-run guide";
  const STEPS = [
    "Welcome",
    "Accounts",
    "Encryption",
    "Signing",
    "Access",
    "Your agent",
    "Done",
  ];

  function expectNoSetupRequest() {
    expect(fetchSetup).not.toHaveBeenCalled();
    expect(putSetupProgress).not.toHaveBeenCalled();
    expect(declineEncryption).not.toHaveBeenCalled();
    expect(completeSetup).not.toHaveBeenCalled();
  }

  it("opens at Welcome on a finished setup with every step open", async () => {
    const user = userEvent.setup();
    renderReview("open");

    await heading(TITLES.welcome);
    expect(screen.getByText("Step 1 of 7")).toBeInTheDocument();
    expect(
      screen.queryByText("Officer is closed until setup is finished"),
    ).not.toBeInTheDocument();
    // The review starts nothing.
    expect(next()).toBeEnabled();
    expect(
      screen.queryByRole("button", { name: "Start setup" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText(
        "Setup finished. REST and MCP answer by the access you chose.",
      ),
    ).toBeInTheDocument();
    for (const name of STEPS) {
      expect(railStep(name, GUIDE)).toBeEnabled();
    }
    expect(railStep("Accounts", GUIDE)).toHaveTextContent("Several accounts");
    expect(railStep("Encryption", GUIDE)).toHaveTextContent("Master key");
    expect(railStep("Signing", GUIDE)).toHaveTextContent("On");
    expect(railStep("Access", GUIDE)).toHaveTextContent("No token");

    await user.click(railStep("Done", GUIDE));

    await heading(TITLES.done);
    expectNoSetupRequest();
  });

  it("moves through every step without writing the progress", async () => {
    const user = userEvent.setup();
    renderReview("open");

    await heading(TITLES.welcome);
    await user.click(next());
    for (const title of [
      TITLES.accounts,
      TITLES.encryption,
      TITLES.signing,
      TITLES.access,
      TITLES.agent,
    ]) {
      await heading(title);
      await user.click(next());
    }

    await heading(TITLES.done);
    expect(
      screen.getAllByRole("button", { name: "Open the panel" }),
    ).toHaveLength(1);
    expectNoSetupRequest();
  });

  describe("encryption", () => {
    it("shows no encryption without asking for the acknowledgement", async () => {
      const user = userEvent.setup();
      mockStorage(false);
      renderReview("open");

      await heading(TITLES.welcome);
      expect(railStep("Encryption", GUIDE)).toHaveTextContent("None");
      await user.click(railStep("Encryption", GUIDE));

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeChecked();
      expect(screen.getByText(NONE_WARNING)).toBeInTheDocument();
      expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
      expect(next()).toBeEnabled();
      await user.click(next());

      await heading(TITLES.signing);
      expectNoSetupRequest();
      await user.click(screen.getByRole("button", { name: "Back" }));

      await heading(TITLES.encryption);
      await user.click(
        screen.getByRole("radio", { name: "With a master key" }),
      );
      expect(
        screen.getByText("Do this in a terminal on this computer"),
      ).toBeInTheDocument();
      expect(screen.queryByText(NONE_WARNING)).not.toBeInTheDocument();
    });

    it("shows a sealed database on the master key, the choice locked", async () => {
      const user = userEvent.setup();
      renderReview("open");

      await heading(TITLES.welcome);
      await user.click(railStep("Encryption", GUIDE));

      await heading(TITLES.encryption);
      expect(
        screen.getByRole("radio", { name: "With a master key" }),
      ).toBeChecked();
      for (const name of ["With a master key", "Without encryption"]) {
        expect(screen.getByRole("radio", { name })).toBeDisabled();
      }
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(screen.queryByText(MASTER_KEY_GUIDE)).not.toBeInTheDocument();
      expect(next()).toBeEnabled();
    });

    it("warns of exposed secrets as Service › Security does", async () => {
      const user = userEvent.setup();
      mockStorage(true, 2);
      renderReview("token", {
        [`GET ${API}/access/tokens/agent`]: () =>
          jsonResponse({
            token: "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w",
          }),
        [`GET ${API}/access/tokens/api`]: () =>
          jsonResponse({
            token: "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w",
          }),
      });

      await openEncryption(user);
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(screen.getByText(EXPOSED_SECRETS)).toBeInTheDocument();
      // The tokens load without a visit to the access step.
      expect(
        await screen.findAllByRole("button", { name: "Regenerate" }),
      ).toHaveLength(2);
    });

    it.each(["agent", "api"] as const)(
      "retries a failed %s token load on Encryption without visiting Access",
      async (failedKind) => {
        const user = userEvent.setup();
        const tokens = {
          agent: "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w",
          api: "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w",
        };
        const retry = deferred<Response>();
        const loadToken = vi
          .fn()
          .mockReturnValueOnce(errorResponse(500, "internal"))
          .mockReturnValueOnce(retry.promise);
        const loadOtherToken = vi.fn(() =>
          jsonResponse({
            token: tokens[failedKind === "agent" ? "api" : "agent"],
          }),
        );
        const loadAccess = vi.fn();
        mockStorage(true, 2);
        renderReview("token", {
          [`GET ${API}/access`]: loadAccess,
          [`GET ${API}/access/tokens/agent`]: loadOtherToken,
          [`GET ${API}/access/tokens/api`]: loadOtherToken,
          [`GET ${API}/access/tokens/${failedKind}`]: loadToken,
        });

        await openEncryption(user);
        expect(await screen.findByText("internal")).toBeVisible();
        expect(screen.getByText(EXPOSED_SECRETS)).toBeInTheDocument();
        expect(
          screen.queryByRole("button", { name: "Regenerate" }),
        ).not.toBeInTheDocument();
        expect(loadToken).toHaveBeenCalledTimes(1);
        expect(loadOtherToken).toHaveBeenCalledTimes(1);

        await user.click(screen.getByRole("button", { name: "Retry" }));

        expect(screen.queryByText("internal")).not.toBeInTheDocument();
        expect(
          screen.queryByRole("button", { name: "Retry" }),
        ).not.toBeInTheDocument();
        expect(screen.getByRole("status", { name: "Loading" })).toBeVisible();
        expect(loadToken).toHaveBeenCalledTimes(2);
        expect(loadOtherToken).toHaveBeenCalledTimes(2);

        await act(async () =>
          retry.resolve(jsonResponse({ token: tokens[failedKind] })),
        );

        expect(
          await screen.findAllByRole("button", { name: "Regenerate" }),
        ).toHaveLength(2);
        expect(
          screen.queryByRole("status", { name: "Loading" }),
        ).not.toBeInTheDocument();
        expect(loadAccess).not.toHaveBeenCalled();
        expectNoSetupRequest();
      },
    );

    it("shows no warning of exposed secrets when none were resealed", async () => {
      const user = userEvent.setup();
      mockStorage(true, 0);
      renderReview("token");

      await openEncryption(user);
      expect(screen.getByText(ENCRYPTION_ON)).toBeInTheDocument();
      expect(screen.queryByText(EXPOSED_SECRETS)).not.toBeInTheDocument();
    });

    it("warns in open access without loading tokens", async () => {
      const user = userEvent.setup();
      const loadToken = vi.fn(() => jsonResponse({ token: AGENT_TOKEN }));
      mockStorage(true, 2);
      renderReview("open", {
        [`GET ${API}/access/tokens/agent`]: loadToken,
        [`GET ${API}/access/tokens/api`]: loadToken,
      });

      await openEncryption(user);
      expect(screen.getByText(EXPOSED_SECRETS)).toBeInTheDocument();
      expect(
        screen.queryByRole("button", { name: "Regenerate" }),
      ).not.toBeInTheDocument();
      expect(loadToken).not.toHaveBeenCalled();
      expectNoSetupRequest();
    });

    /** Opens the encryption step from the rail. */
    async function openEncryption(user: UserEvent) {
      await heading(TITLES.welcome);
      await user.click(railStep("Encryption", GUIDE));
      await heading(TITLES.encryption);
    }

    /** Expects no choice, no guide and no warning on the encryption step. */
    function expectNoChoice() {
      expect(
        screen.queryByRole("radio", { checked: true }),
      ).not.toBeInTheDocument();
      expect(
        screen.queryByText("Do this in a terminal on this computer"),
      ).not.toBeInTheDocument();
      expect(screen.queryByText(NONE_WARNING)).not.toBeInTheDocument();
    }

    it("shows no choice until the storage answers", async () => {
      const user = userEvent.setup();
      const answer = liveStorage({ state: "loading", data: null, error: null });
      renderReview("open");

      await openEncryption(user);
      expectNoChoice();
      expect(screen.getByRole("status", { name: "Loading" })).toBeVisible();

      answer(storage(false));

      expect(
        screen.getByRole("radio", { name: "Without encryption" }),
      ).toBeChecked();
    });

    it("shows no choice over a failed storage load", async () => {
      const user = userEvent.setup();
      vi.mocked(useCredentialStorage).mockReturnValue({
        load: { state: "error", data: null, error: "connection refused" },
        reload: vi.fn(),
      });
      renderReview("open");

      await openEncryption(user);
      expectNoChoice();
      expect(screen.getByRole("alert")).toHaveTextContent("connection refused");
    });
  });

  it("goes on from the accounts step while the account mode loads", async () => {
    const user = userEvent.setup();
    vi.mocked(useAccountMode).mockReturnValue({
      load: { state: "loading", data: null, error: null },
      reload: vi.fn(),
    });
    renderReview("open");

    await heading(TITLES.welcome);
    await user.click(railStep("Accounts", GUIDE));

    await heading(TITLES.accounts);
    expect(next()).toBeEnabled();
    expect(
      screen.getByText("Change later: Service › Account mode"),
    ).toBeInTheDocument();
    await user.click(next());

    await heading(TITLES.encryption);
    expectNoSetupRequest();
  });

  it("opens at the access step under #security, where a mode switch reloads", async () => {
    renderReview("open", {}, "/setup#security");

    await heading(TITLES.access);
    expect(screen.getByText("Step 5 of 7")).toBeInTheDocument();
  });

  it("embeds the access section of Service › Security", async () => {
    const user = userEvent.setup();
    renderReview("open");

    await heading(TITLES.welcome);
    await user.click(railStep("Access", GUIDE));

    await heading(TITLES.access);
    // Its open access holds the mode in force and asks for nothing.
    expect(screen.getByRole("group", { name: "Access" })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: "No token" })).toBeChecked();
    expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Finish setup" }),
    ).not.toBeInTheDocument();
    expect(
      screen.getByText("Change later: Service › Security"),
    ).toBeInTheDocument();
    await user.click(next());

    await heading(TITLES.agent);
    expectNoSetupRequest();
  });

  it.each(["agent", "api"] as const)(
    "retries a failed %s token load when returning to Access",
    async (failedKind) => {
      const user = userEvent.setup();
      const tokens = {
        agent: "pit_mcp_Q2x7bWv9R1t4YzK8pN3sJd6HfL0aEu5cXg2iTo7Vr4w",
        api: "pit_api_Lm4Ts9Qa2Xc7Vn1Bk6Hd3Wy8Pe5Rz0Gu2Jf7Os4Ni9w",
      };
      const loadToken = vi
        .fn()
        .mockRejectedValueOnce(new TypeError("token connection refused"))
        .mockImplementation(() => jsonResponse({ token: tokens[failedKind] }));
      renderReview("token", {
        [`GET ${API}/access`]: () =>
          jsonResponse({
            mode: "token",
            passwordSetAt: "2026-10-01T12:00:00Z",
            sessions: 1,
          }),
        [`GET ${API}/access/tokens/agent`]: () =>
          jsonResponse({ token: tokens.agent }),
        [`GET ${API}/access/tokens/api`]: () =>
          jsonResponse({ token: tokens.api }),
        [`GET ${API}/access/tokens/${failedKind}`]: loadToken,
      });

      await heading(TITLES.welcome);
      await user.click(railStep("Access", GUIDE));
      expect(await screen.findByRole("alert")).toHaveTextContent(
        "token connection refused",
      );
      expect(loadToken).toHaveBeenCalledTimes(1);

      await user.click(railStep("Encryption", GUIDE));
      await heading(TITLES.encryption);
      await user.click(railStep("Access", GUIDE));

      expect(
        await screen.findAllByRole("button", { name: "Regenerate" }),
      ).toHaveLength(2);
      expect(loadToken).toHaveBeenCalledTimes(2);
      expect(screen.queryByRole("alert")).not.toBeInTheDocument();
      expectNoSetupRequest();
    },
  );

  it("leaves the agent step for the panel, with no Connect later", async () => {
    const user = userEvent.setup();
    renderReview("token");

    await heading(TITLES.welcome);
    expect(railStep("Access", GUIDE)).toHaveTextContent("Token and password");
    await user.click(railStep("Your agent", GUIDE));

    await heading(TITLES.agent);
    expect(
      screen.getByText(
        "Give the agent this token only - never the API token or the panel password: with those it steps around its MCP limits.",
      ),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Connect later" }),
    ).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Open the panel" }));

    expect(await screen.findByText("Home")).toBeInTheDocument();
    expectNoSetupRequest();
  });

  it("restarts Officer for a saved account mode and holds the moves", async () => {
    const user = userEvent.setup();
    mockAccountMode(accountMode({ mode: "single" }));
    const { restartService } = renderReview("open", {
      [`PUT ${API}/account-mode`]: () =>
        jsonResponse(accountMode({ restartRequired: true })),
    });

    await heading(TITLES.welcome);
    await user.click(railStep("Accounts", GUIDE));
    await saveSeveralAccounts(user);
    await waitFor(() => expect(restartService).toHaveBeenCalledTimes(1));
    await waitFor(() =>
      expect(screen.queryByRole("alertdialog")).not.toBeInTheDocument(),
    );

    // Officer restarts, so nothing moves on until the page reloads.
    expect(screen.getByRole("button", { name: "Back" })).toBeDisabled();
    expect(
      screen.getByRole("button", { name: "Open the panel" }),
    ).toBeDisabled();
    for (const name of STEPS) {
      expect(railStep(name, GUIDE)).toBeDisabled();
    }
    await user.click(railStep("Welcome", GUIDE));
    expect(
      screen.getByRole("heading", { level: 1, name: TITLES.accounts }),
    ).toBeInTheDocument();
    expectNoSetupRequest();
  });
});
