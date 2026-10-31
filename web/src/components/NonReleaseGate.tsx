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

import {
  AlertTriangle,
  Copy,
  Info,
  Loader2,
  Skull,
  SquarePlus,
} from "lucide-react";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useTranslation } from "react-i18next";

import { chooseDatabase, fetchBuild, type BuildStatus } from "@/api/build";
import { invalid } from "@/api/decode";
import type { LoadState } from "@/api/usePolling";
import { errMessage } from "@/components/AccountDialogs";
import { AuthScreen } from "@/components/AuthScreen";
import { ChoiceCards } from "@/components/ChoiceCards";
import { NonReleaseContext } from "@/components/non-release-context";
import { Notice } from "@/components/Notice";
import { FullScreenError, FullScreenLoading } from "@/components/PageStates";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { useApiClient } from "@/framework";
import { cn } from "@/lib/utils";

const LOADING = { state: "loading", data: null, error: null } as const;

/** The red non-release banner: the sidebar footer and the top strip show it. */
export function NonReleaseBanner() {
  const { t } = useTranslation();
  return (
    <Notice
      tone="danger"
      icon={Skull}
      title={<span className="uppercase">{t("brand.nonRelease")}</span>}
    >
      <p className="font-semibold text-[var(--danger)]">
        {t("nonRelease.banner")}
      </p>
    </Notice>
  );
}

/**
 * Admit the panel only once the build's own database is ready. A non-release
 * build shows its banner in the sidebar footer; on a screen without the
 * sidebar, or below `md` where the sidebar is a closed drawer, it shows the
 * banner as a strip on top.
 */
export function NonReleaseGate({ children }: { children: ReactNode }) {
  const { t } = useTranslation();
  const client = useApiClient();
  const [load, setLoad] = useState<LoadState<BuildStatus>>(LOADING);
  const [attempt, setAttempt] = useState(0);
  const [nonRelease, setNonRelease] = useState(false);
  const [inSidebar, setInSidebar] = useState(false);
  const banner = useMemo(
    () => ({ nonRelease, showInSidebar: setInSidebar }),
    [nonRelease],
  );

  const retry = () => {
    setLoad(LOADING);
    setAttempt((n) => n + 1);
  };

  const accept = useCallback((status: BuildStatus) => {
    if (status.release && status.phase !== "ready") {
      throw invalid("build", "phase", "must be ready for a release build");
    }
    setNonRelease(!status.release);
    setLoad({ state: "ready", data: status, error: null });
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    fetchBuild(client, controller.signal).then(
      (status) => {
        if (!controller.signal.aborted) {
          try {
            accept(status);
          } catch (err) {
            setLoad({ state: "error", data: null, error: errMessage(err) });
          }
        }
      },
      (err: unknown) => {
        if (!controller.signal.aborted) {
          setLoad({ state: "error", data: null, error: errMessage(err) });
        }
      },
    );
    return () => controller.abort();
  }, [client, attempt, accept]);

  useEffect(() => {
    if (load.state !== "ready" || load.data.phase !== "preparing") {
      return;
    }
    const timer = window.setTimeout(() => setAttempt((n) => n + 1), 1000);
    return () => window.clearTimeout(timer);
  }, [load]);

  const content = () => {
    if (load.state === "loading") {
      return <FullScreenLoading />;
    }
    if (load.state === "error") {
      return <FullScreenError message={load.error} onRetry={retry} />;
    }
    const status = load.data;
    switch (status.phase) {
      case "ready":
        return children;
      case "preparing":
        return <FullScreenLoading message={t("nonRelease.preparing")} />;
      case "choice":
      case "failed":
        return (
          <DatabaseChoice
            status={status}
            onAccepted={accept}
            onRefresh={retry}
          />
        );
    }
  };

  return (
    <NonReleaseContext.Provider value={banner}>
      <div className="flex h-dvh flex-col">
        {nonRelease && (
          <div
            className={cn(
              "shrink-0 bg-bg px-[var(--dens-shell-x)] py-3",
              inSidebar && "md:hidden",
            )}
          >
            <NonReleaseBanner />
          </div>
        )}
        {/* The mobile nav is fixed and not portaled; contain it below the banner. */}
        <div
          className={
            nonRelease ? "min-h-0 flex-1 [contain:layout]" : "min-h-0 flex-1"
          }
        >
          {content()}
        </div>
      </div>
    </NonReleaseContext.Provider>
  );
}

function DatabaseChoice({
  status,
  onAccepted,
  onRefresh,
}: {
  status: BuildStatus;
  onAccepted: (status: BuildStatus) => void;
  onRefresh: () => void;
}) {
  const { t } = useTranslation();
  const client = useApiClient();
  const [choice, setChoice] = useState<"copy" | "empty">("empty");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const [error, setError] = useState<string | null>(null);
  const fromRelease = choice === "copy";

  const send = async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError(null);
    try {
      onAccepted(await chooseDatabase(client, fromRelease));
    } catch (err) {
      setError(errMessage(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
      setConfirming(false);
    }
  };

  return (
    <AuthScreen
      wide
      title={t("nonRelease.title")}
      note={t("nonRelease.note")}
      noteIcon={Info}
    >
      <Notice
        tone="danger"
        icon={AlertTriangle}
        title={t("nonRelease.noticeTitle")}
      >
        <p>{t("nonRelease.notice")}</p>
      </Notice>
      {status.phase === "failed" && (
        <Notice
          tone="danger"
          icon={AlertTriangle}
          title={t("nonRelease.failureTitle")}
          role="alert"
        >
          <p className="break-words">{status.error}</p>
        </Notice>
      )}
      {error !== null && (
        <Notice
          tone="danger"
          icon={AlertTriangle}
          title={t("nonRelease.failureTitle")}
          role="alert"
          action={
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onRefresh}
            >
              {t("actions.refresh")}
            </Button>
          }
        >
          <p className="break-words">{error}</p>
        </Notice>
      )}
      <ChoiceCards
        label={t("nonRelease.title")}
        value={choice}
        onChange={setChoice}
        disabled={busy}
        options={[
          {
            value: "copy",
            icon: Copy,
            title: t("nonRelease.copy.title"),
            description: t("nonRelease.copy.description"),
            unavailable: status.releaseDatabaseExists
              ? undefined
              : t("nonRelease.noReleaseDatabase"),
          },
          {
            value: "empty",
            icon: SquarePlus,
            title: t("nonRelease.empty.title"),
            description: t("nonRelease.empty.description"),
          },
        ]}
      />
      <Button type="button" disabled={busy} onClick={() => setConfirming(true)}>
        {t("actions.continue")}
      </Button>
      <AlertDialog
        open={confirming}
        onOpenChange={(open) => {
          if (!busy) setConfirming(open);
        }}
      >
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("nonRelease.confirm.title")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t(
                fromRelease
                  ? "nonRelease.confirm.copy"
                  : "nonRelease.confirm.empty",
              )}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel disabled={busy}>
              {t("actions.cancel")}
            </AlertDialogCancel>
            <AlertDialogAction
              variant="danger"
              disabled={busy}
              onClick={(event) => {
                event.preventDefault();
                void send();
              }}
            >
              {busy && (
                <Loader2 aria-hidden className="h-3.5 w-3.5 animate-spin" />
              )}
              {t("nonRelease.confirm.action")}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </AuthScreen>
  );
}
