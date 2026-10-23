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

import { ExternalLink, ShieldCheck, TerminalSquare } from "lucide-react";
import { useMemo } from "react";
import { useTranslation } from "react-i18next";

import { Section } from "@/components/welcome/Section";

/**
 * What Officer is, with links to its website, issue tracker, discussions and
 * the OpenPit engine, then the ways it can be used.
 */
export function WelcomeIntro() {
  const { t } = useTranslation();

  const links = useMemo(
    () => [
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
    ],
    [t],
  );

  return (
    <>
      <Section icon={ShieldCheck} label={t("welcome.sections.product")}>
        <p>{t("welcome.product.copy")}</p>
        <div className="mt-3 grid gap-2 sm:grid-cols-2">
          {links.map((link) => (
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
      </Section>

      <Section icon={TerminalSquare} label={t("welcome.sections.uses")}>
        <div className="grid gap-2 sm:grid-cols-2">
          {["dropCopy", "preTrade", "surfaces", "operations"].map((item) => (
            <div
              key={item}
              className="rounded-card border border-border bg-surface-2 p-3"
            >
              <div className="text-xs font-bold text-text">
                {t(`welcome.useCases.${item}.title`)}
              </div>
              <p className="mt-1 text-xs">
                {t(`welcome.useCases.${item}.copy`)}
              </p>
            </div>
          ))}
        </div>
      </Section>
    </>
  );
}
