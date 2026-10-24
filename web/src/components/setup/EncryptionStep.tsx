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
  KeyRound,
  Loader2,
  LockOpen,
  ShieldCheck,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import type { CredentialStorage } from "@/api/types";
import type { LoadState } from "@/api/usePolling";
import {
  Acknowledgement,
  type AcknowledgementState,
} from "@/components/Acknowledgement";
import { ChoiceCards } from "@/components/ChoiceCards";
import { MasterKeyGuide } from "@/components/MasterKeyGuide";
import { Notice } from "@/components/Notice";
import {
  CompactStaleState,
  ErrorBanner,
  LoadingStatus,
} from "@/components/PageStates";
import { AccessTokensProvider } from "@/components/security/AccessSection";
import { ExposedSecretsNotice } from "@/components/security/ExposedSecretsNotice";
import { panelSession } from "@/lib/panelSession";

export type EncryptionChoice = "key" | "none";

/**
 * The encryption step of the setup wizard: the choice between a master key
 * and none, then whether Officer sealed its database yet, with the guide to
 * the key while it has not, or the warning of stored keys left unencrypted.
 * Once the database is sealed the choice is the key and cannot change: the
 * wizard cannot remove the key. Under either choice, how the credential
 * storage load stands; with no choice known yet, that alone.
 */
export function EncryptionStep({
  choice,
  onChoose,
  storage,
  sealed,
  acknowledgement,
  disabled,
  exposedSecrets,
}: {
  /** The choice shown, or null while the one in force is not known. */
  choice: EncryptionChoice | null;
  onChoose: (choice: EncryptionChoice) => void;
  /** The polled credential storage. */
  storage: LoadState<CredentialStorage>;
  sealed: boolean;
  /**
   * The confirmation the warning of no encryption ends in. The first run asks
   * for it to decline encryption; its review shows the choice in force and
   * asks for none. The checkbox follows `disabled`.
   */
  acknowledgement?: Omit<AcknowledgementState, "disabled">;
  disabled: boolean;
  /**
   * Whether a sealed database warns of the secrets sealing re-encrypted from
   * plain text, as Service › Security does. Only the review does: the first
   * run cannot open the pages it links to. The warning gets its own
   * AccessTokensProvider, which loads the tokens at once in `token` access.
   */
  exposedSecrets: boolean;
}) {
  const { t } = useTranslation("setup");
  const { t: ts } = useTranslation("service");
  return (
    <div className="space-y-4">
      {choice !== null && (
        <ChoiceCards
          label={t("encryption.title")}
          options={[
            {
              value: "key",
              title: t("encryption.key.title"),
              description: t("encryption.key.description"),
              icon: KeyRound,
              badge: ts("access.recommended"),
            },
            {
              value: "none",
              title: t("encryption.none.title"),
              description: t("encryption.none.description"),
              icon: LockOpen,
            },
          ]}
          value={choice}
          onChange={onChoose}
          disabled={disabled || sealed}
        />
      )}
      {choice === "key" && (
        <div className="space-y-4">
          {/* Once sealed, its commands would overwrite the key in force. */}
          {storage.state === "ready" && !sealed && <MasterKeyGuide />}
          {/* One live region for both states, so sealing is announced. */}
          <div role="status">
            {storage.state === "ready" &&
              (sealed ? (
                <Notice
                  tone="ok"
                  icon={ShieldCheck}
                  title={t("encryption.on")}
                />
              ) : (
                <div className="flex items-center gap-2.5 rounded-card border border-dashed border-border px-3.5 py-2.5 text-xs text-muted-lt">
                  <Loader2
                    aria-hidden="true"
                    className="h-3.5 w-3.5 animate-spin"
                  />
                  <span>{t("encryption.waiting")}</span>
                </div>
              ))}
          </div>
          {exposedSecrets &&
            storage.state === "ready" &&
            sealed &&
            storage.data.resealedSecrets > 0 && (
              <AccessTokensProvider
                allowed={panelSession().accessMode === "token"}
              >
                <ExposedSecretsNotice
                  count={storage.data.resealedSecrets}
                  showTokenStatus
                />
              </AccessTokensProvider>
            )}
        </div>
      )}
      {choice === "none" && (
        <Notice
          tone="danger"
          icon={AlertTriangle}
          title={t("encryption.noneTitle")}
        >
          <p>{t("encryption.noneWho")}</p>
          <p>{t("encryption.noneWhat")}</p>
          <p>{t("encryption.noneBanner")}</p>
          {acknowledgement !== undefined && (
            <Acknowledgement {...acknowledgement} disabled={disabled}>
              {t("encryption.noneAck")}
            </Acknowledgement>
          )}
        </Notice>
      )}
      {/* Either choice goes on only once the storage answered. */}
      {storage.state === "loading" && <LoadingStatus />}
      {storage.state === "error" && <ErrorBanner message={storage.error} />}
      <CompactStaleState load={storage} />
    </div>
  );
}
