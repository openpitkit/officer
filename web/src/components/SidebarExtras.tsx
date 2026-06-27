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

import { AlertTriangle, ExternalLink, Info } from "lucide-react";
import { useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { useMarketData } from "@/api/useMarketData";
import { useService } from "@/api/useService";
import { WelcomeDialog } from "@/components/WelcomeDialog";
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

export function RestartRequiredNavEntry() {
  const { t } = useTranslation();
  const { close } = useSidebar();
  const { load } = useMarketData();
  const restartRequired =
    load.state === "ready" && load.data.restartRequired;
  if (!restartRequired) {
    return null;
  }
  return (
    <Link
      to="/market-data"
      onClick={close}
      className="mb-1 flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] border border-[var(--warn)]/40 bg-[var(--warn)]/10 px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] font-semibold text-[var(--warn)] transition-colors hover:bg-[var(--warn)]/15"
    >
      <AlertTriangle className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
      <span className="flex-1 text-left">
        {t("nav.restartRequired")}
      </span>
    </Link>
  );
}

export function NonReleaseNavEntry() {
  const { t } = useTranslation();
  const { close } = useSidebar();
  const { load } = useService();
  const isNonRelease =
    load.state === "ready" && load.data.release === false;
  if (!isNonRelease) {
    return null;
  }
  return (
    <Link
      to="/service"
      onClick={close}
      className="mt-1 flex items-center gap-[var(--dens-nav-gap)] rounded-[3px] border border-accent bg-accent px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-[length:var(--dens-footer-fz)] font-bold uppercase tracking-[0.07em] text-bg transition-colors hover:border-accent-2 hover:bg-accent-2"
    >
      <AlertTriangle className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
      <span className="flex-1 text-left">{t("brand.nonRelease")}</span>
    </Link>
  );
}

export function AboutNavEntry() {
  const { t } = useTranslation();
  const [welcomeOpen, setWelcomeOpen] = useState(false);
  const aboutLinks = [
    {
      label: t("about.links.website"),
      href: "https://officer.openpit.dev?officer",
    },
    {
      label: t("about.links.issues"),
      href: "https://github.com/openpitkit/officer/issues?officer",
    },
    {
      label: t("about.links.discussions"),
      href: "https://github.com/openpitkit/officer/discussions?officer",
    },
    {
      label: t("about.links.openpit"),
      href: "https://openpit.dev?officer",
    },
  ];

  return (
    <>
      <Dialog>
        <DialogTrigger asChild>
          <button
            type="button"
            className="mt-1 flex w-full items-center gap-[var(--dens-nav-gap)] rounded-[3px] px-[var(--dens-nav-px)] py-[var(--dens-nav-py)] text-left text-[length:var(--dens-footer-fz)] text-muted transition-colors hover:bg-surface-hover hover:text-muted-lt"
          >
            <Info className="h-[var(--dens-footer-icon)] w-[var(--dens-footer-icon)] shrink-0" />
            <span className="flex-1">{t("about.title")}</span>
          </button>
        </DialogTrigger>
        <DialogContent className="max-w-xl">
          <DialogHeader>
            <DialogTitle>{t("about.title")}</DialogTitle>
            <DialogDescription>{t("about.description")}</DialogDescription>
          </DialogHeader>
          <div className="space-y-4 text-sm">
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
            <div className="grid gap-2 sm:grid-cols-2">
              {aboutLinks.map((link) => (
                <a
                  key={link.href}
                  href={link.href}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="inline-flex items-center justify-between gap-3 rounded-card border border-border bg-surface-2 px-3 py-2 text-xs text-muted-lt transition-colors hover:border-border-hover hover:bg-card-hover-bg hover:text-accent"
                >
                  <span>{link.label}</span>
                  <ExternalLink className="h-3.5 w-3.5 shrink-0" />
                </a>
              ))}
            </div>
            <Button
              type="button"
              variant="outline"
              className="w-full"
              onClick={() => {
                setWelcomeOpen(true);
              }}
            >
              <Info />
              {t("about.openWelcome")}
            </Button>
          </div>
        </DialogContent>
      </Dialog>
      <WelcomeDialog open={welcomeOpen} onOpenChange={setWelcomeOpen} />
    </>
  );
}

