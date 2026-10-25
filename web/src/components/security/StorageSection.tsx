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

import { KeyRound, Lock, LockOpen, Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type { CredentialStorage, SigningKeysStatus } from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { useSigningKeys } from "@/api/useSigningKeys";
import { MasterKeyGuide } from "@/components/MasterKeyGuide";
import {
  CompactStaleState,
  ErrorBanner,
  LoadingStatus,
} from "@/components/PageStates";
import { ExposedSecretsNotice } from "@/components/security/ExposedSecretsNotice";
import { SecurityRow } from "@/components/security/SecurityRow";
import {
  accountModeValue,
  encryptionValue,
  signingValue,
} from "@/components/security/storageValues";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { accountScope } from "@/lib/accountScope";

/**
 * Whether stored credentials are sealed under a master key, and since when;
 * while they are not, the master-key guide under the value.
 */
function EncryptionRow({ storage }: { storage: LoadState<CredentialStorage> }) {
  const { t } = useTranslation("service");
  return (
    <SecurityRow label={t("security.storage.encryption")}>
      {storage.state === "loading" && <LoadingStatus />}
      {storage.state === "error" && <ErrorBanner message={storage.error} />}
      {storage.state === "ready" &&
        (storage.data.credentialsSealed ? (
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="ok">
              <Lock aria-hidden="true" className="h-3 w-3" />
              {t("security.storage.masterKey")}
            </Badge>
            <span>{encryptionValue(t, storage.data)}</span>
            <CompactStaleState load={storage} />
          </div>
        ) : (
          <div className="flex flex-col gap-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="warn">
                <LockOpen aria-hidden="true" className="h-3 w-3" />
                {t("security.storage.noMasterKey")}
              </Badge>
              <span>{encryptionValue(t, storage.data)}</span>
              <CompactStaleState load={storage} />
            </div>
            <MasterKeyGuide />
          </div>
        ))}
    </SecurityRow>
  );
}

/**
 * Whether Officer signs its decisions: off by the setting, or on with the
 * active key that signs them, or on with no key to sign with.
 */
function SigningRow({ signing }: { signing: LoadState<SigningKeysStatus> }) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const value =
    signing.state === "ready"
      ? signingValue(
          t,
          signing.data.eSignEnabled,
          signing.data.keys.find((key) => key.active) ?? null,
        )
      : null;
  return (
    <SecurityRow
      label={t("security.storage.signing")}
      actions={
        <Button asChild variant="outline" size="sm">
          <Link to="/signing-keys">
            <KeyRound aria-hidden="true" />
            {tc("nav.signingKeys")}
          </Link>
        </Button>
      }
    >
      {signing.state === "loading" && <LoadingStatus />}
      {signing.state === "error" && <ErrorBanner message={signing.error} />}
      {value !== null && (
        <div className="flex flex-wrap items-center gap-2">
          <span>{value}</span>
          <CompactStaleState load={signing} />
        </div>
      )}
    </SecurityRow>
  );
}

/** The account mode the running process enforces. */
function AccountModeRow() {
  const { t } = useTranslation("service");
  const { showsAccounts, account } = accountScope();
  return (
    <SecurityRow
      label={t("security.storage.accountMode")}
      actions={
        <Button asChild variant="outline" size="sm">
          <Link to="/service#account-mode">
            <Users aria-hidden="true" />
            {t("security.storage.openAccountMode")}
          </Link>
        </Button>
      }
    >
      <p>{accountModeValue(t, showsAccounts, account)}</p>
    </SecurityRow>
  );
}

/**
 * The storage section of Service › Security: encryption of stored keys,
 * decision signing and the account mode, under the notice of exposed secrets
 * while sealing re-encrypted any. It regenerates tokens through the
 * AccessTokensProvider around it.
 */
export function StorageSection() {
  const { t } = useTranslation("service");
  const { load: storage } = useCredentialStorage();
  const { load: signing } = useSigningKeys();
  return (
    <Card>
      <CardHeader>
        <CardTitle>{t("security.storage.title")}</CardTitle>
      </CardHeader>
      <CardContent className="flex flex-col gap-y-3">
        {storage.state === "ready" && storage.data.resealedSecrets > 0 && (
          <ExposedSecretsNotice count={storage.data.resealedSecrets} />
        )}
        <div>
          <EncryptionRow storage={storage} />
          <SigningRow signing={signing} />
          <AccountModeRow />
        </div>
      </CardContent>
    </Card>
  );
}
