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

import type { AccessGrantBlock } from "./accessGrantBlock";
import { WarnHint } from "./WarnHint";

/** Reason a grant is refused, plus exactly one way to fix it: a button when
 *  the caller can reach the fee account field itself, a router link when it
 *  can only point to the page that has it. `feeAccountHref` is an in-app
 *  router path (e.g. "/trading-connections"), followed client-side, so the
 *  link variant needs a router above it. */
export type AccessBlockedHintProps = {
  reason: AccessGrantBlock;
  /** Provider title interpolated into the sentence. */
  providerTitle: string;
} & (
  | { onSetFeeAccount: () => void; feeAccountHref?: never }
  | { feeAccountHref: string; onSetFeeAccount?: never }
);

/** States why a grant is blocked and offers the "Set fee account" action. */
export function AccessBlockedHint(props: AccessBlockedHintProps) {
  const { t } = useTranslation("venues");
  const reason = t(`accessBlocked.${props.reason}`, {
    provider: props.providerTitle,
  });
  return props.feeAccountHref !== undefined ? (
    <WarnHint reason={reason} to={props.feeAccountHref}>
      {t("actions.setFeeAccount")}
    </WarnHint>
  ) : (
    <WarnHint reason={reason} onClick={props.onSetFeeAccount}>
      {t("actions.setFeeAccount")}
    </WarnHint>
  );
}
