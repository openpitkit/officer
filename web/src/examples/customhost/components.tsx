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

import { type ReactNode } from "react";
import { useTranslation } from "react-i18next";

import { AuthProvider } from "@openpit/officer-web";

import { hiddenPermission } from "./ids";

export function CustomHostAuthProvider({ children }: { children: ReactNode }) {
  return (
    <AuthProvider
      hasPermission={(permission) => permission !== hiddenPermission}
    >
      {children}
    </AuthProvider>
  );
}

export function HostReferencePage() {
  const { t } = useTranslation();
  return (
    <section aria-label="customhost-page">{t("customhost:page.title")}</section>
  );
}

export function ReplacementHostReferencePage() {
  const { t } = useTranslation();
  return (
    <section aria-label="customhost-replacement-page">
      {t("customhost:page.replacementTitle")}
    </section>
  );
}

export function HostReferenceWidget() {
  const { t } = useTranslation();
  return (
    <aside aria-label="customhost-widget">{t("customhost:widget.title")}</aside>
  );
}

export function ReplacementHostReferenceWidget() {
  const { t } = useTranslation();
  return (
    <aside aria-label="customhost-replacement-widget">
      {t("customhost:widget.replacementTitle")}
    </aside>
  );
}
