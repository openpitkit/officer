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

import { Loader2 } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { fetchSession, type AccessMode, type SessionState } from "@/api/access";
import {
  fetchAccountMode,
  type AccountFeatures,
  type AccountModeState,
} from "@/api/accountMode";
import { usePolling, type LoadState } from "@/api/usePolling";
import { errMessage } from "@/components/AccountDialogs";
import { ErrorState, StaleState } from "@/components/PageStates";
import { useApiClient } from "@/framework";
import { setAccountScope } from "@/lib/accountScope";
import { onCheckSession, setPanelSession } from "@/lib/panelSession";
import { Login } from "@/pages/Login";
import { PasswordRecovery } from "@/pages/PasswordRecovery";

const LOADING: LoadState<AccountModeState> = {
  state: "loading",
  data: null,
  error: null,
};

const StartupAccountModeContext = createContext<AccountModeState | null>(null);

/**
 * The account mode as the gate loaded it before the shell rendered; later
 * polls do not change it.
 *
 * @throws Error when used outside StartupGate.
 */
// eslint-disable-next-line react-refresh/only-export-components -- Gate hook.
export function useStartupAccountMode(): AccountModeState {
  const state = useContext(StartupAccountModeContext);
  if (state === null) {
    throw new Error("useStartupAccountMode is used outside StartupGate");
  }
  return state;
}

/** Where the session step of the gate stands. */
type SessionStep =
  | { state: "loading" }
  | { state: "error"; error: string }
  | { state: "login" }
  | { state: "recovery" }
  | { state: "admitted" };

const SESSION_LOADING: SessionStep = { state: "loading" };
const LOGIN: SessionStep = { state: "login" };
const RECOVERY: SessionStep = { state: "recovery" };
const ADMITTED: SessionStep = { state: "admitted" };

/**
 * Holds what must be settled before the shell renders: the session, then the
 * account mode. In `token` access mode without a session it shows the login
 * or the password recovery screen in place of `children` and keeps the URL.
 * `checkSession()` asks `GET /session` again and shows the login screen once
 * the session is gone, or reloads the page when the access mode switched since
 * the browser was admitted. One check runs at a time: an ask during it makes
 * its answer stale and starts one more check after it settles, and an
 * admission makes the answers of earlier checks stale. Once admitted it renders
 * `children` only after the account scope of the running process is loaded,
 * then keeps polling it: other features reload the page, as the process was
 * restarted into another mode, and a failed poll marks the scope stale.
 */
export function StartupGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const client = useApiClient();
  const [step, setStep] = useState<SessionStep>(SESSION_LOADING);
  // Bumped by every ask for a check and by every admission: the answer of a
  // check started before the latest bump speaks for an older state.
  const generation = useRef(0);
  // The check in flight, which unmounting or a new client aborts.
  const inFlight = useRef<AbortController | null>(null);
  // Set by an ask while a check is in flight: exactly one more check starts
  // once that one settles.
  const followUp = useRef(false);
  // The access mode the browser was admitted under, once it was.
  const admittedMode = useRef<AccessMode | null>(null);

  const admit = useCallback((accessMode: AccessMode) => {
    generation.current += 1;
    admittedMode.current = accessMode;
    // Set before the state update, so the session is in place before the
    // shell renders for the first time.
    setPanelSession(accessMode);
    setStep(ADMITTED);
  }, []);

  const apply = useCallback(
    ({ accessMode, authenticated }: SessionState) => {
      if (
        admittedMode.current !== null &&
        accessMode !== admittedMode.current
      ) {
        // The access mode switched while the page lived: start it afresh.
        window.location.reload();
        return;
      }
      if (accessMode === "token" && !authenticated) {
        // A check that lands while the recovery screen is open keeps it.
        setStep((current) => (current === RECOVERY ? current : LOGIN));
        return;
      }
      admit(accessMode);
    },
    [admit],
  );

  // Runs checks one at a time, as long as asks keep arriving during them.
  const run = useCallback(async () => {
    do {
      followUp.current = false;
      const controller = new AbortController();
      const asked = generation.current;
      inFlight.current = controller;
      try {
        const state = await fetchSession(client, controller.signal);
        if (controller.signal.aborted) {
          return;
        }
        if (generation.current === asked) {
          apply(state);
        }
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        if (generation.current === asked) {
          setStep({ state: "error", error: errMessage(err) });
        }
      }
      inFlight.current = null;
    } while (followUp.current);
  }, [client, apply]);

  const check = useCallback(() => {
    generation.current += 1;
    if (inFlight.current !== null) {
      followUp.current = true;
      return;
    }
    void run();
  }, [run]);

  useEffect(() => {
    check();
    const unsubscribe = onCheckSession(check);
    return () => {
      unsubscribe();
      // Unmounted, or a new client: the answer in flight is for neither.
      inFlight.current?.abort();
      inFlight.current = null;
      followUp.current = false;
    };
  }, [check]);

  if (step.state === "loading") {
    return <FullScreenLoading />;
  }
  if (step.state === "error") {
    return (
      <FullScreenError
        title={t("auth:session.loadError")}
        message={step.error}
        onRetry={() => {
          setStep(SESSION_LOADING);
          check();
        }}
      />
    );
  }
  if (step.state === "login") {
    return (
      <Login
        onLoggedIn={() => admit("token")}
        onForgotPassword={() => setStep(RECOVERY)}
      />
    );
  }
  if (step.state === "recovery") {
    return (
      <PasswordRecovery
        onPasswordReset={() => admit("token")}
        onBack={() => setStep(LOGIN)}
      />
    );
  }
  return <AccountModeStep>{children}</AccountModeStep>;
}

/**
 * Renders `children` only once the account scope of the running process is
 * loaded, then keeps polling it through `AccountScopeWatch`.
 */
function AccountModeStep({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const client = useApiClient();
  const [load, setLoad] = useState<LoadState<AccountModeState>>(LOADING);
  const [attempt, setAttempt] = useState(0);

  // One request with no poll timer to abort it, so an answer slower than the
  // poll interval still loads the scope.
  useEffect(() => {
    const controller = new AbortController();
    fetchAccountMode(client, controller.signal).then(
      (state) => {
        if (controller.signal.aborted) {
          return;
        }
        // Set before the state update, so the scope is in place before the
        // children render for the first time.
        setAccountScope(state.features);
        setLoad({ state: "ready", data: state, error: null });
      },
      (err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        const message = err instanceof Error ? err.message : String(err);
        setLoad({ state: "error", data: null, error: message });
      },
    );
    return () => controller.abort();
  }, [client, attempt]);

  if (load.state === "loading") {
    return <FullScreenLoading />;
  }
  if (load.state === "error") {
    return (
      <FullScreenError
        title={t("states.accountModeLoadError")}
        message={load.error}
        onRetry={() => {
          setLoad(LOADING);
          setAttempt((n) => n + 1);
        }}
      />
    );
  }
  return (
    <StartupAccountModeContext.Provider value={load.data}>
      {children}
      <AccountScopeWatch loaded={load.data.features} />
    </StartupAccountModeContext.Provider>
  );
}

/** The whole viewport while a startup step loads. */
function FullScreenLoading() {
  const { t } = useTranslation();
  return (
    <div
      role="status"
      aria-busy="true"
      aria-label={t("states.loading")}
      className="flex h-dvh w-full items-center justify-center bg-bg"
    >
      <Loader2 className="h-5 w-5 animate-spin text-muted-lt" />
    </div>
  );
}

/** The whole viewport when a startup step could not load, with a retry. */
function FullScreenError({
  title,
  message,
  onRetry,
}: {
  title: string;
  message: string;
  onRetry: () => void;
}) {
  return (
    <div className="flex h-dvh w-full items-center justify-center bg-bg p-6 text-text">
      <div className="w-full max-w-lg">
        <ErrorState title={title} message={message} onRetry={onRetry} />
      </div>
    </div>
  );
}

/** Polls the account mode after the scope loaded with `loaded`. */
function AccountScopeWatch({ loaded }: { loaded: AccountFeatures }) {
  const client = useApiClient();
  const fetcher = useCallback(
    async (signal: AbortSignal) => {
      const { features } = await fetchAccountMode(client, signal);
      if (
        features.accounts !== loaded.accounts ||
        features.account !== loaded.account
      ) {
        window.location.reload();
      }
      return features;
    },
    [client, loaded],
  );
  const { load, reload } = usePolling(fetcher);

  if (load.error === null) {
    return null;
  }
  // The scope keeps the loaded features whatever the poll answers, so a failed
  // poll, the first included, marks those features stale.
  return (
    <div className="fixed bottom-4 right-4 z-30 w-80 max-w-[calc(100vw-2rem)]">
      <StaleState
        load={{ state: "ready", data: loaded, error: load.error }}
        reload={reload}
      />
    </div>
  );
}
