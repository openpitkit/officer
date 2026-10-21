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

import { Loader2, LogOut, ShieldCheck } from "lucide-react";
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { flushSync } from "react-dom";
import { useTranslation } from "react-i18next";

import {
  endAllSessions,
  fetchAccess,
  fetchAccessToken,
  isAccessModeConflict,
  putAccessMode,
  regenerateAccessToken,
  type AccessMode,
  type AccessState,
  type AccessTokenKind,
} from "@/api/access";
import type { LoadState } from "@/api/usePolling";
import { AccessModePanel } from "@/components/AccessModePanel";
import { errMessage } from "@/components/AccountDialogs";
import {
  ErrorBanner,
  ErrorState,
  LoadingStatus,
} from "@/components/PageStates";
import { PasswordChangeDialog } from "@/components/security/PasswordChangeDialog";
import { SecurityRow } from "@/components/security/SecurityRow";
import { SwitchToOpenDialog } from "@/components/security/SwitchToOpenDialog";
import { TokenRow } from "@/components/security/TokenRow";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ApiError, useApiClient } from "@/framework";
import { formatDate } from "@/i18n/format";
import { checkSession, panelSession } from "@/lib/panelSession";

/**
 * Reloads the page onto this tab after a switch of the access mode: the
 * startup gate reads the mode in force, and Service selects the tab from the
 * hash.
 */
function reloadOnSecurityTab() {
  window.history.replaceState(window.history.state, "", "#security");
  window.location.reload();
}

/**
 * `open` access: the access mode choice inline with "No token" selected, and
 * turning on token access from it. The choice asks for no acknowledgement:
 * this is the mode in force, not a new one.
 */
function OpenAccess() {
  const { t } = useTranslation("service");
  const client = useApiClient();
  const passwordField = useRef<HTMLInputElement>(null);
  const [mode, setMode] = useState<AccessMode>("open");
  const [password, setPassword] = useState<string | null>(null);
  // Bumped by a failed attempt: the panel mounts anew, without the password.
  const [attempt, setAttempt] = useState(0);
  const [switching, setSwitching] = useState(false);
  const [passwordError, setPasswordError] = useState<string | undefined>(
    undefined,
  );
  const [error, setError] = useState<string | null>(null);

  const turnOn = async () => {
    if (password === null) {
      return;
    }
    setSwitching(true);
    setPasswordError(undefined);
    setError(null);
    try {
      await putAccessMode(client, { mode: "token", password });
    } catch (err) {
      const code = err instanceof ApiError ? err.code : null;
      // Committed at once, the fields mounted anew and enabled, before the
      // focus moves to the refused one, so its description reads it out.
      flushSync(() => {
        setPassword(null);
        setAttempt((n) => n + 1);
        setSwitching(false);
        if (code === "precondition") {
          setPasswordError(errMessage(err));
        } else if (!isAccessModeConflict(err)) {
          setError(errMessage(err));
        }
      });
      if (code === "precondition") {
        passwordField.current?.focus();
      } else if (isAccessModeConflict(err)) {
        checkSession();
      }
      return;
    }
    // The answer set a session cookie for this browser, which the startup
    // gate admits under the new mode.
    reloadOnSecurityTab();
  };

  return (
    <SecurityRow label={t("security.access.mode")}>
      <div className="space-y-4">
        <AccessModePanel
          key={attempt}
          mode={mode}
          onModeChange={setMode}
          onPasswordChange={setPassword}
          passwordError={passwordError}
          passwordRef={passwordField}
          disabled={switching}
        />
        {mode === "token" && (
          <Button
            type="button"
            size="sm"
            disabled={switching || password === null}
            onClick={() => {
              void turnOn();
            }}
          >
            {switching ? (
              <Loader2 aria-hidden="true" className="animate-spin" />
            ) : (
              <ShieldCheck aria-hidden="true" />
            )}
            {t("security.access.turnOn")}
          </Button>
        )}
        {error !== null && <ErrorBanner message={error} />}
      </div>
    </SecurityRow>
  );
}

/** When the panel password was set and how many sessions are live. */
type TokenAccessInfo = Extract<AccessState, { mode: "token" }>;

const LOADING = { state: "loading", data: null, error: null } as const;

/** Both tokens of `token` access and the action that regenerates one. */
interface AccessTokens {
  tokens: LoadState<Record<AccessTokenKind, string>>;
  /**
   * Whether GET /access has confirmed `token` access, after which the tokens
   * load: the token routes answer 409 in `open`.
   */
  allowed: boolean;
  /** Reports that GET /access has confirmed `token` access. */
  allow: () => void;
  /**
   * Issues a new value of `kind` and keeps it in `tokens`; rejects with the
   * request's error.
   */
  regenerate: (kind: AccessTokenKind) => Promise<void>;
}

const AccessTokensContext = createContext<AccessTokens | null>(null);

/**
 * Holds both tokens for the Access section's rows and for every other place
 * on Service › Security that regenerates one, so a new value shows wherever
 * the token does. Both values are fetched once, after GET /access has
 * confirmed `token` access, so Copy writes the value inside the operator's
 * click, and live only in this state.
 */
export function AccessTokensProvider({ children }: { children: ReactNode }) {
  const client = useApiClient();
  const [allowed, setAllowed] = useState(false);
  const [tokens, setTokens] =
    useState<LoadState<Record<AccessTokenKind, string>>>(LOADING);

  useEffect(() => {
    if (!allowed) {
      return undefined;
    }
    const controller = new AbortController();
    Promise.all([
      fetchAccessToken(client, "agent", controller.signal),
      fetchAccessToken(client, "api", controller.signal),
    ]).then(
      ([agent, api]) => {
        if (!controller.signal.aborted) {
          setTokens({ state: "ready", data: { agent, api }, error: null });
        }
      },
      (err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (isAccessModeConflict(err)) {
          checkSession();
          return;
        }
        setTokens({ state: "error", data: null, error: errMessage(err) });
      },
    );
    return () => controller.abort();
  }, [client, allowed]);

  const allow = useCallback(() => setAllowed(true), []);

  const regenerate = async (kind: AccessTokenKind) => {
    let token: string;
    try {
      token = await regenerateAccessToken(client, kind);
    } catch (err) {
      if (isAccessModeConflict(err)) {
        checkSession();
        return;
      }
      throw err;
    }
    setTokens((current) =>
      current.state === "ready"
        ? { ...current, data: { ...current.data, [kind]: token } }
        : current,
    );
  };

  return (
    <AccessTokensContext.Provider
      value={{ tokens, allowed, allow, regenerate }}
    >
      {children}
    </AccessTokensContext.Provider>
  );
}

/**
 * Both tokens and their regenerate action.
 *
 * @throws Error when used outside AccessTokensProvider.
 */
// eslint-disable-next-line react-refresh/only-export-components -- Provider hook.
export function useAccessTokens(): AccessTokens {
  const value = useContext(AccessTokensContext);
  if (value === null) {
    throw new Error("useAccessTokens is used outside AccessTokensProvider");
  }
  return value;
}

/**
 * `token` access: the mode, the panel password, the browser sessions and both
 * tokens. The access info and the tokens load apart, so a failure of one
 * leaves the other usable.
 */
function TokenAccess() {
  const { t } = useTranslation("service");
  const client = useApiClient();
  const [access, setAccess] = useState<LoadState<TokenAccessInfo>>(LOADING);
  // Bumped by Retry and by a password change, which moves the date and ends
  // the other sessions.
  const [accessLoads, setAccessLoads] = useState(0);
  const {
    tokens,
    allowed: tokensAllowed,
    allow: allowTokens,
    regenerate,
  } = useAccessTokens();
  const [switchingToOpen, setSwitchingToOpen] = useState(false);
  const [changingPassword, setChangingPassword] = useState(false);
  const [passwordChanged, setPasswordChanged] = useState(false);
  const [loggingOut, setLoggingOut] = useState(false);
  const [logOutError, setLogOutError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    fetchAccess(client, controller.signal).then(
      (state) => {
        if (controller.signal.aborted) {
          return;
        }
        if (state.mode !== "token") {
          checkSession();
          return;
        }
        setAccess({ state: "ready", data: state, error: null });
        allowTokens();
      },
      (err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (isAccessModeConflict(err)) {
          checkSession();
          return;
        }
        setAccess({ state: "error", data: null, error: errMessage(err) });
      },
    );
    return () => controller.abort();
  }, [client, accessLoads, allowTokens]);

  // Drops the access info on screen at once: after a password change its date
  // and session count are no longer true.
  const reloadAccess = () => {
    setAccess(LOADING);
    setAccessLoads((n) => n + 1);
  };

  const logOutEverywhere = async () => {
    setLoggingOut(true);
    setLogOutError(null);
    try {
      await endAllSessions(client);
    } catch (err) {
      setLogOutError(errMessage(err));
      return;
    } finally {
      setLoggingOut(false);
    }
    checkSession();
  };

  return (
    <>
      <SecurityRow
        label={t("security.access.mode")}
        actions={
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setSwitchingToOpen(true)}
          >
            {t("security.access.switchToOpen")}
          </Button>
        }
      >
        <Badge variant="ok">
          <ShieldCheck aria-hidden="true" className="h-3 w-3" />
          {t("security.access.token")}
        </Badge>
      </SecurityRow>
      {access.state === "loading" && (
        <div className="py-3">
          <LoadingStatus />
        </div>
      )}
      {access.state === "error" && (
        <ErrorState message={access.error} onRetry={reloadAccess} />
      )}
      {access.state === "ready" && (
        <>
          <SecurityRow
            label={t("security.access.password")}
            actions={
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  setPasswordChanged(false);
                  setChangingPassword(true);
                }}
              >
                {t("security.access.change")}
              </Button>
            }
          >
            <p>
              {t("security.access.passwordSet", {
                date: formatDate(access.data.passwordSetAt),
              })}
            </p>
          </SecurityRow>
          <SecurityRow
            label={t("security.access.sessions")}
            actions={
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={loggingOut}
                onClick={() => {
                  void logOutEverywhere();
                }}
              >
                <LogOut aria-hidden="true" />
                {t("security.access.logOutEverywhere")}
              </Button>
            }
          >
            <p>
              {t("security.access.sessionCount", {
                count: access.data.sessions,
              })}
            </p>
            {logOutError !== null && <ErrorBanner message={logOutError} />}
          </SecurityRow>
        </>
      )}
      {passwordChanged && (
        // Outside the password row, so it stays while the access info reloads
        // or fails to.
        <p role="status" className="py-2 text-xs text-[var(--ok)]">
          {t("security.access.passwordChanged")}
        </p>
      )}
      {tokensAllowed && tokens.state === "loading" && (
        <div className="py-3">
          <LoadingStatus />
        </div>
      )}
      {tokens.state === "error" && <ErrorBanner message={tokens.error} />}
      {tokens.state === "ready" && (
        <>
          <TokenRow
            kind="agent"
            token={tokens.data.agent}
            onRegenerate={() => regenerate("agent")}
          />
          <TokenRow
            kind="api"
            token={tokens.data.api}
            onRegenerate={() => regenerate("api")}
          />
        </>
      )}
      {switchingToOpen && (
        <SwitchToOpenDialog
          onClose={() => setSwitchingToOpen(false)}
          onSwitched={reloadOnSecurityTab}
        />
      )}
      {changingPassword && (
        <PasswordChangeDialog
          onClose={() => setChangingPassword(false)}
          onChanged={() => {
            setChangingPassword(false);
            setPasswordChanged(true);
            reloadAccess();
          }}
        />
      )}
    </>
  );
}

/**
 * The Access section of Service › Security, under the access mode the startup
 * gate admitted this browser with. In `open` it holds the access mode choice
 * and asks for nothing that only `token` access has; in `token` it shows and
 * changes the mode, the panel password, the browser sessions and both tokens,
 * which it holds in the AccessTokensProvider around it.
 */
export function AccessSection() {
  const { t } = useTranslation("service");
  const { accessMode } = panelSession();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("security.access.title")}</CardTitle>
      </CardHeader>
      <CardContent>
        {accessMode === "open" ? <OpenAccess /> : <TokenAccess />}
      </CardContent>
    </Card>
  );
}
