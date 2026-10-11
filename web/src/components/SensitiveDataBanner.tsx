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

import { AlertTriangle } from "lucide-react";
import { useTranslation } from "react-i18next";

import { useCredentialStorage } from "@/api/useCredentialStorage";
import { MasterKeyLink } from "@/components/MasterKeyLink";
import { Notice } from "@/components/Notice";

/** Warns on every page while stored keys of any kind sit in plain text. */
export function SensitiveDataBanner() {
  const { t } = useTranslation();
  const { load } = useCredentialStorage();

  if (
    load.state !== "ready" ||
    !(load.data.plaintextTradingKeys || load.data.plaintextMarketDataKeys)
  )
    return null;

  return (
    <Notice tone="danger" icon={AlertTriangle} title={t("sensitiveData.title")}>
      <p>{t("sensitiveData.detail")}</p>
      <p>
        <MasterKeyLink />
      </p>
    </Notice>
  );
}
