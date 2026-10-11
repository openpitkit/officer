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

import { useTranslation } from "react-i18next";

const MASTER_KEY_GUIDE =
  "https://github.com/openpitkit/officer/blob/main/docs/configuration.md#master-key";

/** Opens the master-key setup guide in a new tab. */
export function MasterKeyLink() {
  const { t } = useTranslation();
  return (
    <a
      href={MASTER_KEY_GUIDE}
      target="_blank"
      rel="noopener noreferrer"
      className="underline underline-offset-4"
    >
      {t("sensitiveData.masterKeyLink")}
    </a>
  );
}
