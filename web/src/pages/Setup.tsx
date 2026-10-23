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

import { AlertTriangle, Lock } from "lucide-react";
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";
import { useLocation, useNavigate } from "react-router-dom";

import type { AccessMode } from "@/api/access";
import { useAccountMode } from "@/api/accountMode";
import {
  completeSetup,
  declineEncryption,
  fetchSetup,
  isSetupComplete,
  putSetupProgress,
  type SetupCompleteRequest,
  type SetupState,
  type SetupStep,
} from "@/api/setup";
import type { LoadState } from "@/api/usePolling";
import { AccessModePanel } from "@/components/AccessModePanel";
import { AccountModePanel } from "@/components/AccountModeChoice";
import { errMessage } from "@/components/AccountDialogs";
import { ConnectAgent } from "@/components/ConnectAgent";
import { Notice } from "@/components/Notice";
import {
  ErrorBanner,
  FullScreenError,
  FullScreenLoading,
} from "@/components/PageStates";
import {
  SigningKeysPanel,
  useSigningKeysPanel,
} from "@/components/SigningKeysPanel";
import { useStartupAdmit } from "@/components/StartupGate";
import {
  AccessSection,
  AccessTokensProvider,
} from "@/components/security/AccessSection";
import { DoneSummary } from "@/components/setup/DoneSummary";
import {
  EncryptionStep,
  type EncryptionChoice,
} from "@/components/setup/EncryptionStep";
import { SetupRail } from "@/components/setup/SetupRail";
import {
  StepFooter,
  type StepFooterProps,
} from "@/components/setup/StepFooter";
import {
  WIZARD_STEPS,
  type DecisionStep,
  type WizardStep,
} from "@/components/setup/steps";
import { Button } from "@/components/ui/button";
import { LinkButton } from "@/components/venues";
import { WelcomeIntro } from "@/components/welcome/WelcomeIntro";
import { WelcomeQuickStart } from "@/components/welcome/WelcomeQuickStart";
import { ApiError, useApiClient, useOfficerApi } from "@/framework";
import { checkSession } from "@/lib/panelSession";
import { usePasswordAttempt } from "@/lib/usePasswordAttempt";

const LOADING: LoadState<SetupState> = {
  state: "loading",
  data: null,
  error: null,
};

/** Whether the server keeps `step` as the position the wizard resumes at. */
function isStored(step: WizardStep): step is SetupStep {
  return step !== "agent" && step !== "done";
}

/**
 * The first-run setup: a full-screen wizard outside the panel shell. It opens
 * at the step the server stored. Once setup turns out to be complete, as when
 * it was finished in another browser, it shows nothing and asks the startup
 * gate to check the session again.
 */
export function Setup() {
  const client = useApiClient();
  const [load, setLoad] = useState<LoadState<SetupState>>(LOADING);
  const [attempt, setAttempt] = useState(0);
  const [complete, setComplete] = useState(false);

  const completedElsewhere = useCallback(() => {
    setComplete(true);
    checkSession();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    fetchSetup(client, controller.signal).then(
      (data) => {
        if (!controller.signal.aborted) {
          setLoad({ state: "ready", data, error: null });
        }
      },
      (err: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (isSetupComplete(err)) {
          completedElsewhere();
          return;
        }
        setLoad({ state: "error", data: null, error: errMessage(err) });
      },
    );
    return () => controller.abort();
  }, [client, attempt, completedElsewhere]);

  if (complete) {
    return null;
  }
  if (load.state === "loading") {
    return <FullScreenLoading />;
  }
  if (load.state === "error") {
    return (
      <FullScreenError
        message={load.error}
        onRetry={() => {
          setLoad(LOADING);
          setAttempt((n) => n + 1);
        }}
      />
    );
  }
  return (
    <Wizard
      pass={{
        kind: "firstRun",
        initial: load.data,
        onCompletedElsewhere: completedElsewhere,
      }}
    />
  );
}

/**
 * The review of a completed setup: the same wizard, open at Welcome, or at the
 * access step under `#security`, with every step reachable, each step showing
 * the settings in force through the parts the first run embeds. It sends no
 * setup request: once setup is complete the server refuses them all.
 */
export function SetupReview({ accessMode }: { accessMode: AccessMode }) {
  const { hash } = useLocation();
  // A switch of the access mode reloads the page at #security, the address of
  // the security settings, which the review shows on the access step.
  const start = hash === "#security" ? "access" : "welcome";
  return <Wizard pass={{ kind: "review", accessMode, start }} />;
}

/**
 * How the wizard runs: the first run over the stored setup state, which hands
 * over to the startup gate once setup turns out to be complete, or the review
 * of a completed setup under the access mode in force, open at `start`.
 */
type WizardPass =
  | {
      kind: "firstRun";
      initial: SetupState;
      onCompletedElsewhere: () => void;
    }
  | { kind: "review"; accessMode: AccessMode; start: WizardStep };

/** The masthead, the body and the footer of the current step. */
type StepView = {
  title: string;
  lead: string;
  body: ReactNode;
  footer: StepFooterProps;
};

/**
 * The seven steps, over the stored setup state in the first run and over the
 * settings in force in the review. It holds the account mode poll and one
 * signing keys controller for the whole wizard: the steps embed them, and the
 * rail and the summary read them.
 */
function Wizard({ pass }: { pass: WizardPass }) {
  const { t } = useTranslation("setup");
  const { t: tc } = useTranslation();
  const { t: ts } = useTranslation("service");
  const client = useApiClient();
  const { restartService } = useOfficerApi();
  const admit = useStartupAdmit();
  const navigate = useNavigate();
  const accountPoll = useAccountMode();
  const signing = useSigningKeysPanel();
  const {
    attempt,
    password,
    setPassword,
    passwordError,
    passwordRef,
    clearPasswordError,
    refuse,
  } = usePasswordAttempt();

  const review = pass.kind === "review";
  const declineRecorded =
    pass.kind === "firstRun" && pass.initial.encryptionDeclinedAt !== null;

  const [step, setStep] = useState<WizardStep>(
    pass.kind === "firstRun" ? pass.initial.step : pass.start,
  );
  const [moving, setMoving] = useState(false);
  // Set while a move runs, so no second one starts before it settles.
  const busy = useRef(false);
  const [error, setError] = useState<string | null>(null);
  // The review opens on a completed setup.
  const [finished, setFinished] = useState(review);
  const [declinedHere, setDeclined] = useState(declineRecorded);
  // Null until the operator picks one.
  const [encryptionPick, setEncryptionPick] = useState<EncryptionChoice | null>(
    null,
  );
  // The server recorded the acknowledgement along with the decline.
  const [encryptionAck, setEncryptionAck] = useState(declineRecorded);
  const [accessMode, setAccessMode] = useState<AccessMode>(
    pass.kind === "review" ? pass.accessMode : "token",
  );
  const [accessAck, setAccessAck] = useState(false);

  // A completed setup leaves the database unsealed only by a decline, which
  // the review knows once the storage answers.
  const declined = review ? signing.storage.state === "ready" : declinedHere;
  const accounts =
    accountPoll.load.state === "ready" ? accountPoll.load.data : null;
  const accountsStored =
    accounts !== null && accounts.mode !== null && !accounts.restartRequired;
  // A sealed database wins over a pick and over a recorded decline. The
  // review shows no choice until the storage answers.
  const encryption: EncryptionChoice | null =
    review && signing.storage.state !== "ready"
      ? null
      : signing.sealed
        ? "key"
        : (encryptionPick ?? (declined ? "none" : "key"));
  // Nothing is decided before the storage answers whether it is sealed.
  const encryptionDecided =
    signing.storage.state === "ready" &&
    (encryption === "key" ? signing.sealed : encryptionAck);
  const accessDecided = accessMode === "token" ? password !== null : accessAck;
  const current = WIZARD_STEPS.indexOf(step);

  const security = t("place", {
    section: tc("nav.service"),
    tab: ts("tabs.security"),
  });
  const places: Record<DecisionStep, string> = {
    accounts: t("place", {
      section: tc("nav.service"),
      tab: ts("tabs.accountMode"),
    }),
    encryption: security,
    signing: tc("nav.signingKeys"),
    access: security,
    agent: tc("nav.mcpAccess"),
  };

  const openPanel = () => void navigate("/");

  // Whether `err` found setup complete, which in the first run hands over to
  // the startup gate.
  const completedElsewhere = (err: unknown) => {
    if (pass.kind !== "firstRun" || !isSetupComplete(err)) {
      return false;
    }
    pass.onCompletedElsewhere();
    return true;
  };

  const fail = (err: unknown) => {
    if (completedElsewhere(err)) {
      return;
    }
    setError(errMessage(err));
  };

  // Moves to `target` once `before` and, in the first run for a step the
  // server keeps, the stored position succeed; on a failure it stays and
  // shows the error.
  const go = async (target: WizardStep, before?: () => Promise<void>) => {
    if (busy.current) {
      return;
    }
    busy.current = true;
    setMoving(true);
    setError(null);
    try {
      await before?.();
      if (pass.kind === "firstRun" && isStored(target)) {
        await putSetupProgress(client, target);
      }
    } catch (err) {
      fail(err);
      return;
    } finally {
      busy.current = false;
      setMoving(false);
    }
    setStep(target);
    if (step === "access") {
      // The fields went away with the access panel.
      setPassword(null);
    }
  };

  // In the first run the position moves on before the restart, so the wizard
  // resumes at the encryption step once Officer is back. No move starts from
  // here on: once Officer restarts, the startup gate reloads the page.
  const restart = async () => {
    busy.current = true;
    setMoving(true);
    try {
      if (pass.kind === "firstRun") {
        await putSetupProgress(client, "encryption");
      }
      await restartService();
    } catch (err) {
      busy.current = false;
      setMoving(false);
      completedElsewhere(err);
      throw err;
    }
  };

  const finish = async () => {
    const request: SetupCompleteRequest | null =
      accessMode === "open"
        ? { access: "open", acknowledged: true }
        : password === null
          ? null
          : { access: "token", password };
    if (request === null || busy.current) {
      return;
    }
    busy.current = true;
    setMoving(true);
    setError(null);
    clearPasswordError();
    try {
      await completeSetup(client, request);
    } catch (err) {
      busy.current = false;
      if (completedElsewhere(err)) {
        return;
      }
      // The only invalid part of a token request is the password.
      const passwordRefused =
        request.access === "token" &&
        err instanceof ApiError &&
        err.status === 422;
      refuse(err, passwordRefused, () => {
        setMoving(false);
        if (!passwordRefused) {
          setError(errMessage(err));
        }
      });
      return;
    }
    busy.current = false;
    // In token access the answer set a session cookie for this browser.
    admit(accessMode);
    setFinished(true);
    setMoving(false);
    setStep("agent");
  };

  // Before completion the first run shows no value for the current step and
  // the steps after it.
  const ahead = (index: number) => !finished && index >= current;

  const railValue = (id: WizardStep, index: number): string | null => {
    switch (id) {
      case "accounts":
        return accounts?.mode ? ts(`accountMode.modes.${accounts.mode}`) : null;
      case "encryption":
        if (ahead(index)) {
          return null;
        }
        if (signing.sealed) {
          return ts("security.storage.masterKey");
        }
        return declined ? ts("security.storage.noMasterKey") : null;
      case "signing":
        if (ahead(index) || signing.load.state !== "ready") {
          return null;
        }
        return signing.eSignEnabled
          ? t("signing.on")
          : ts("security.storage.signingOff");
      case "access":
        return finished ? ts(`access.modes.${accessMode}.title`) : null;
      case "welcome":
      case "agent":
      case "done":
        return null;
    }
  };

  const view = (): StepView => {
    switch (step) {
      case "welcome":
        return {
          title: t("welcome.title"),
          lead: t("welcome.lead"),
          body: (
            <>
              <WelcomeIntro />
              {!review && (
                <Notice
                  tone="info"
                  icon={Lock}
                  title={t("welcome.closedTitle")}
                >
                  <p>{t("welcome.closedBody")}</p>
                </Notice>
              )}
            </>
          ),
          footer: {
            next: () => void go("accounts"),
            nextLabel: review ? undefined : t("welcome.start"),
          },
        };
      case "accounts":
        return {
          title: t("accounts.title"),
          lead: t("accounts.lead"),
          body: (
            <AccountModePanel
              poll={accountPoll}
              restart={restart}
              disabled={moving}
              onSaved={accountPoll.reload}
            />
          ),
          // The review takes no decision here.
          footer: {
            ...(review || accountsStored
              ? { where: places.accounts }
              : { note: t("accounts.saveFirst") }),
            back: () => void go("welcome"),
            next: () => void go("encryption"),
            nextDisabled: !review && !accountsStored,
          },
        };
      case "encryption":
        return {
          title: t("encryption.title"),
          lead: t("encryption.lead"),
          body: (
            <EncryptionStep
              choice={encryption}
              onChoose={setEncryptionPick}
              storage={signing.storage}
              sealed={signing.sealed}
              acknowledgement={
                review
                  ? undefined
                  : { acknowledged: encryptionAck, onChange: setEncryptionAck }
              }
              disabled={moving}
              exposedSecrets={review}
            />
          ),
          footer: {
            back: () => void go("accounts"),
            // The review takes no decision here.
            next: () =>
              void go(
                "signing",
                !review && encryption === "none"
                  ? async () => {
                      await declineEncryption(client);
                      setDeclined(true);
                    }
                  : undefined,
              ),
            nextDisabled: !review && !encryptionDecided,
          },
        };
      case "signing":
        return {
          title: t("signing.title"),
          lead: t("signing.lead"),
          body: (
            <SigningKeysPanel
              controller={signing}
              masterKeyAction={
                <LinkButton onClick={() => void go("encryption")}>
                  {t("signing.backToEncryption")}
                </LinkButton>
              }
            />
          ),
          footer: {
            where: places.signing,
            back: () => void go("encryption"),
            next: () => void go("access"),
          },
        };
      case "access":
        if (review) {
          // The access mode in force changes as on Service › Security.
          return {
            title: t("access.title"),
            lead: t("access.lead"),
            body: (
              <AccessTokensProvider>
                <AccessSection />
              </AccessTokensProvider>
            ),
            footer: {
              where: places.access,
              back: () => void go("signing"),
              next: () => void go("agent"),
            },
          };
        }
        return {
          title: t("access.title"),
          lead: t("access.lead"),
          body: (
            <AccessModePanel
              key={attempt}
              mode={accessMode}
              onModeChange={setAccessMode}
              onPasswordChange={setPassword}
              passwordError={passwordError}
              passwordRef={passwordRef}
              disabled={moving}
              acknowledgement={{
                acknowledged: accessAck,
                onChange: setAccessAck,
              }}
            />
          ),
          footer: {
            where: places.access,
            back: () => void go("signing"),
            next: () => void finish(),
            nextLabel: t("access.finish"),
            nextDisabled: !accessDecided,
          },
        };
      case "agent":
        return {
          title: t("agent.title"),
          lead: t("agent.lead"),
          body: (
            <>
              <ConnectAgent />
              {accessMode === "token" && (
                <Notice
                  tone="warn"
                  icon={AlertTriangle}
                  title={t("agent.warning")}
                />
              )}
            </>
          ),
          footer: {
            where: places.agent,
            extra: review ? undefined : (
              <Button
                type="button"
                variant="ghost"
                onClick={() => void go("done")}
              >
                {t("agent.later")}
              </Button>
            ),
            next: () => void go("done"),
          },
        };
      case "done":
        return {
          title: t("done.title"),
          lead: t("done.lead"),
          body: (
            <>
              <DoneSummary
                accounts={accountPoll.load}
                signing={signing}
                accessMode={accessMode}
                places={places}
              />
              <WelcomeQuickStart />
            </>
          ),
          footer: {
            note: t("done.reopen"),
            next: openPanel,
            nextLabel: t("done.open"),
          },
        };
    }
  };

  const { title, lead, body, footer } = view();
  // In the review every step before the last offers the way out to the panel.
  const extra =
    review && step !== "done" ? (
      <Button
        type="button"
        variant="ghost"
        disabled={moving}
        onClick={openPanel}
      >
        {t("done.open")}
      </Button>
    ) : (
      footer.extra
    );
  return (
    <div className="flex h-dvh w-full flex-col overflow-y-auto bg-bg text-text md:flex-row md:overflow-hidden">
      <SetupRail
        review={review}
        current={current}
        finished={finished}
        values={WIZARD_STEPS.map(railValue)}
        busy={moving}
        onJump={(target) => {
          if (target !== step) {
            void go(target);
          }
        }}
      />
      <main className="flex min-w-0 flex-1 flex-col md:overflow-hidden">
        <div className="flex-1 md:overflow-y-auto">
          <div className="mx-auto w-full max-w-3xl space-y-5 px-4 py-6 md:px-10 md:py-8">
            <div className="ledger-masthead space-y-1 pb-3">
              <p className="text-[0.6875rem] font-medium uppercase tracking-[0.07em] text-muted">
                {step === "welcome" && !review
                  ? t("firstStart")
                  : t("stepOf", { n: current + 1, total: WIZARD_STEPS.length })}
              </p>
              <h1 className="ledger-title text-2xl">{title}</h1>
              <p className="text-sm text-muted-lt">{lead}</p>
            </div>
            {body}
            {error !== null && (
              <ErrorBanner message={error} onDismiss={() => setError(null)} />
            )}
          </div>
        </div>
        <StepFooter {...footer} extra={extra} busy={moving} />
      </main>
    </div>
  );
}
