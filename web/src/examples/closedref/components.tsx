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

export function ClosedReferenceAuthProvider({
  children,
}: {
  children: ReactNode;
}) {
  return (
    <AuthProvider
      hasPermission={(permission) => permission !== hiddenPermission}
    >
      {children}
    </AuthProvider>
  );
}

export function PrivateReferencePage() {
  const { t } = useTranslation();
  return (
    <section aria-label="closed-reference-page">
      {t("closedref:page.title")}
    </section>
  );
}

export function ReplacementPrivateReferencePage() {
  const { t } = useTranslation();
  return (
    <section aria-label="closed-reference-replacement-page">
      {t("closedref:page.replacementTitle")}
    </section>
  );
}

export function PrivateReferenceWidget() {
  const { t } = useTranslation();
  return (
    <aside aria-label="closed-reference-widget">
      {t("closedref:widget.title")}
    </aside>
  );
}

export function ReplacementPrivateReferenceWidget() {
  const { t } = useTranslation();
  return (
    <aside aria-label="closed-reference-replacement-widget">
      {t("closedref:widget.replacementTitle")}
    </aside>
  );
}
