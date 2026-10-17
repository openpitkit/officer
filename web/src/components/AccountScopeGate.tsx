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
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import {
  fetchAccountMode,
  type AccountFeatures,
  type AccountModeState,
} from "@/api/accountMode";
import { usePolling, type LoadState } from "@/api/usePolling";
import { ErrorState, StaleState } from "@/components/PageStates";
import { useApiClient } from "@/framework";
import { setAccountScope } from "@/lib/accountScope";

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
 * @throws Error when used outside AccountScopeGate.
 */
// eslint-disable-next-line react-refresh/only-export-components -- Gate hook.
export function useStartupAccountMode(): AccountModeState {
  const state = useContext(StartupAccountModeContext);
  if (state === null) {
    throw new Error("useStartupAccountMode is used outside AccountScopeGate");
  }
  return state;
}

/**
 * Render `children` only once the account scope of the running process is
 * loaded, then keep polling it: other features reload the page, as the process
 * was restarted into another mode, and a failed poll marks the scope stale.
 */
export function AccountScopeGate({ children }: { children: ReactNode }) {
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
  if (load.state === "error") {
    return (
      <div className="flex h-dvh w-full items-center justify-center bg-bg p-6 text-text">
        <div className="w-full max-w-lg">
          <ErrorState
            title={t("states.accountModeLoadError")}
            message={load.error}
            onRetry={() => {
              setLoad(LOADING);
              setAttempt((n) => n + 1);
            }}
          />
        </div>
      </div>
    );
  }
  return (
    <StartupAccountModeContext.Provider value={load.data}>
      {children}
      <AccountScopeWatch loaded={load.data.features} />
    </StartupAccountModeContext.Provider>
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
