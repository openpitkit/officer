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

import { useTranslation } from "react-i18next";

function platformNewTabHintKey(): string {
  if (typeof navigator === "undefined") {
    return "rowActions.openInNewTabHint";
  }
  const maybeUAData = navigator as Navigator & {
    userAgentData?: { platform?: string };
  };
  const platform =
    maybeUAData.userAgentData?.platform ?? navigator.platform ?? navigator.userAgent;
  const normalized = platform.toLowerCase();
  if (
    normalized.includes("mac") ||
    normalized.includes("iphone") ||
    normalized.includes("ipad") ||
    normalized.includes("ipod")
  ) {
    return "rowActions.openInNewTabHintMac";
  }
  if (
    normalized.includes("win") ||
    normalized.includes("linux") ||
    normalized.includes("android") ||
    normalized.includes("cros") ||
    normalized.includes("x11")
  ) {
    return "rowActions.openInNewTabHintCtrl";
  }
  return "rowActions.openInNewTabHint";
}

export function useOpenInNewTabHint(): string {
  const { t } = useTranslation("common");
  return t(platformNewTabHintKey());
}
