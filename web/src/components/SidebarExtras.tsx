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
  Info,
  LogOut,
  Skull,
  type LucideIcon,
} from "lucide-react";
import { useLayoutEffect, useState, type ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link, useLocation } from "react-router-dom";

import { logOut } from "@/api/access";
import {
  storesPlaintextKeys,
  useCredentialStorage,
} from "@/api/useCredentialStorage";
import { useMarketData } from "@/api/useMarketData";
import { errMessage } from "@/components/AccountDialogs";
import { useNonRelease } from "@/components/non-release-context";
import { NonReleaseBanner } from "@/components/NonReleaseGate";
import { Notice } from "@/components/Notice";
import { CompactStaleState, ErrorBanner } from "@/components/PageStates";
import { ProductLinks } from "@/components/ProductLinks";
import { useSidebar } from "@/components/sidebar-context";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog";
import { useApiClient } from "@/framework";
import { checkSession } from "@/lib/panelSession";
import { cn } from "@/lib/utils";

export function RestartRequiredNavEntry() {
  const { pathname } = useLocation();
  if (pathname === "/market-data") {
    return null;
  }
  return <RestartRequiredNavIndicator />;
}

function RestartRequiredNavIndicator() {
  const { t } = useTranslation();
  const { close, collapsed } = useSidebar();
  const { load } = useMarketData();
  const restartRequired = load.state === "ready" && load.data.restartRequired;
  if (!restartRequired) {
    return null;
  }
  return (
    <Link
      to="/market-data"
      onClick={close}
      title={
        collapsed
          ? t("nav.restartRequired") +
            (load.error === null ? "" : `: ${load.error}`)
          : undefined
      }
      className={cn(
        "mb-1 flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] border border-[var(--warn)]/40 bg-[var(--warn)]/10 px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] font-semibold text-[var(--warn)] transition-colors hover:bg-[var(--warn)]/15",
        collapsed && "justify-center gap-0 px-2",
      )}
    >
      <AlertTriangle className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
      <span className={collapsed ? "sr-only" : "flex-1 text-left"}>
        {t("nav.restartRequired")}
      </span>
      {collapsed ? (
        <span className="sr-only">
          <CompactStaleState load={load} />
        </span>
      ) : (
        <CompactStaleState load={load} />
      )}
    </Link>
  );
}

function SidebarPlaqueLink({
  to,
  label,
  icon: Icon,
  tone,
  children,
}: {
  to: string;
  label: string;
  icon: LucideIcon;
  tone: "warn" | "danger";
  children: ReactNode;
}) {
  const { close, collapsed } = useSidebar();
  return (
    <Link
      to={to}
      onClick={close}
      title={collapsed ? label : undefined}
      aria-label={collapsed ? label : undefined}
      className={cn(
        "mt-1 block rounded-card transition-opacity hover:opacity-90 focus-visible:outline-hidden focus-visible:ring-2 focus-visible:ring-ring",
        collapsed && "flex justify-center border p-2",
        collapsed &&
          (tone === "danger"
            ? "border-[var(--danger)]/40 bg-[var(--danger)]/10 text-[var(--danger)]"
            : "border-[var(--warn)]/40 bg-[var(--warn)]/10 text-[var(--warn)]"),
      )}
    >
      {collapsed ? (
        <Icon aria-hidden="true" className="h-4 w-4 shrink-0" />
      ) : (
        children
      )}
    </Link>
  );
}

/**
 * The non-release banner in the sidebar footer, on every shell page including
 * `/service`, which it links to. While it is mounted, the gate keeps its top
 * strip only below `md`, where the sidebar is a closed drawer.
 */
export function NonReleaseNavEntry() {
  const { t } = useTranslation();
  const { nonRelease, showInSidebar } = useNonRelease();
  useLayoutEffect(() => {
    if (!nonRelease) return;
    showInSidebar(true);
    return () => showInSidebar(false);
  }, [nonRelease, showInSidebar]);
  if (!nonRelease) {
    return null;
  }
  return (
    <SidebarPlaqueLink
      to="/service"
      label={`${t("brand.nonRelease")}: ${t("nonRelease.banner")}`}
      icon={Skull}
      tone="danger"
    >
      <NonReleaseBanner />
    </SidebarPlaqueLink>
  );
}

/**
 * The yellow plaque in the sidebar footer while stored keys sit in plain text;
 * it leads to Service › Security, where the master key is set. That page shows
 * the full red notice itself and owns the request there, so the plaque stays
 * off it.
 */
export function SensitiveDataNavEntry() {
  const { pathname } = useLocation();
  if (pathname === "/service") {
    return null;
  }
  return <SensitiveDataNavPlaque />;
}

function SensitiveDataNavPlaque() {
  const { t } = useTranslation();
  const { load } = useCredentialStorage();
  if (!storesPlaintextKeys(load)) {
    return null;
  }
  return (
    <SidebarPlaqueLink
      to="/service#security"
      label={`${t("sensitiveData.title")}. ${t("sensitiveData.masterKeyLink")}`}
      icon={AlertTriangle}
      tone="warn"
    >
      <Notice tone="warn" icon={AlertTriangle} title={t("sensitiveData.title")}>
        <p>{t("sensitiveData.masterKeyLink")}</p>
      </Notice>
    </SidebarPlaqueLink>
  );
}

/**
 * Ends this browser's session with `DELETE /session`, then asks the startup
 * gate to check the session, which shows the login screen. A failure shows in
 * the entry and leaves the session as it was.
 */
export function LogoutNavEntry() {
  const { t } = useTranslation();
  const { collapsed } = useSidebar();
  const client = useApiClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const leave = async () => {
    setBusy(true);
    setError(null);
    try {
      await logOut(client);
    } catch (err) {
      setError(errMessage(err));
      return;
    } finally {
      setBusy(false);
    }
    checkSession();
  };

  return (
    <div>
      <button
        type="button"
        disabled={busy}
        title={collapsed ? t("nav.logOut") : undefined}
        onClick={() => {
          void leave();
        }}
        className={cn(
          "flex w-full items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-left text-[length:var(--dens-footer-fz)] text-muted transition-colors hover:bg-surface-hover hover:text-muted-lt disabled:pointer-events-none disabled:opacity-50",
          collapsed && "justify-center gap-0 px-2",
        )}
      >
        <LogOut className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
        <span className={collapsed ? "sr-only" : "flex-1"}>
          {t("nav.logOut")}
        </span>
      </button>
      {error !== null &&
        (collapsed ? (
          <div
            role="alert"
            title={error}
            className="mt-1 flex justify-center text-[var(--danger)]"
          >
            <AlertTriangle className="h-4 w-4" aria-hidden="true" />
            <span className="sr-only">{error}</span>
          </div>
        ) : (
          <ErrorBanner className="mt-1" message={error} />
        ))}
    </div>
  );
}

export function AboutNavEntry() {
  const { t } = useTranslation();
  const { collapsed } = useSidebar();

  return (
    <Dialog>
      <DialogTrigger asChild>
        <button
          type="button"
          title={collapsed ? t("about.title") : undefined}
          className={cn(
            "mt-1 flex w-full items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-left text-[length:var(--dens-footer-fz)] text-muted transition-colors hover:bg-surface-hover hover:text-muted-lt",
            collapsed && "justify-center gap-0 px-2",
          )}
        >
          <Info className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
          <span className={collapsed ? "sr-only" : "flex-1"}>
            {t("about.title")}
          </span>
        </button>
      </DialogTrigger>
      <DialogContent className="max-w-xl">
        <DialogHeader>
          <DialogTitle>{t("about.title")}</DialogTitle>
          <DialogDescription>{t("about.description")}</DialogDescription>
        </DialogHeader>
        <div className="flex flex-col gap-y-4 text-sm">
          <div className="rounded-card border border-border bg-surface-2 p-3">
            <div className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("about.product")}
            </div>
            <div className="mt-1 text-text">{t("brand.name")}</div>
          </div>
          <div className="rounded-card border border-border bg-surface-2 p-3">
            <div className="text-[0.6875rem] font-bold uppercase tracking-[0.07em] text-muted">
              {t("about.copyrightLabel")}
            </div>
            <div className="mt-1 text-text">{t("about.copyright")}</div>
          </div>
          <ProductLinks />
          <Button asChild variant="outline" className="w-full">
            <Link to="/setup">
              <Info />
              {t("about.openWelcome")}
            </Link>
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
