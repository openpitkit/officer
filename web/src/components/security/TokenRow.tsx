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

import { useState } from "react";
import { useTranslation } from "react-i18next";

import type { AccessTokenKind } from "@/api/access";
import { CopyButton } from "@/components/CopyButton";
import { RevealButton } from "@/components/RevealButton";
import { RegenerateTokenButton } from "@/components/security/RegenerateTokenButton";
import { SecurityRow } from "@/components/security/SecurityRow";
import { maskAccessToken } from "@/lib/accessTokenMask";

/**
 * A token of Service › Security: masked until Show, Copy, and Regenerate after
 * a confirmation. The caller holds the value in its state and passes it here.
 */
export function TokenRow({
  kind,
  token,
  onRegenerate,
}: {
  kind: AccessTokenKind;
  token: string;
  /**
   * Issues a new value, which the caller passes back as `token`; rejects with
   * the request's error.
   */
  onRegenerate: () => Promise<void>;
}) {
  const { t } = useTranslation("service");
  const [shown, setShown] = useState(false);

  return (
    <SecurityRow
      label={t(`security.tokens.${kind}.label`)}
      actions={
        <>
          <RevealButton shown={shown} onToggle={() => setShown(!shown)} />
          <CopyButton text={token} />
          <RegenerateTokenButton kind={kind} onRegenerate={onRegenerate} />
        </>
      }
    >
      <p className="break-all font-mono text-text">
        {shown ? token : maskAccessToken(kind, token)}
      </p>
      <p>{t(`security.tokens.${kind}.note`)}</p>
    </SecurityRow>
  );
}
