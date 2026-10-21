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

import { AlertTriangle, KeyRound, Lock, LockOpen, Users } from "lucide-react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type { CredentialStorage, SigningKeysStatus } from "@/api/types";
import { useCredentialStorage } from "@/api/useCredentialStorage";
import type { LoadState } from "@/api/usePolling";
import { useSigningKeys } from "@/api/useSigningKeys";
import { MasterKeyGuide } from "@/components/MasterKeyGuide";
import { Notice } from "@/components/Notice";
import {
  CompactStaleState,
  ErrorBanner,
  LoadingStatus,
} from "@/components/PageStates";
import { useAccessTokens } from "@/components/security/AccessSection";
import { RegenerateTokenButton } from "@/components/security/RegenerateTokenButton";
import { SecurityRow } from "@/components/security/SecurityRow";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { formatDateTime } from "@/i18n/format";
import { accountScope } from "@/lib/accountScope";
import { panelSession } from "@/lib/panelSession";

/**
 * Values that sealing re-encrypted from plain text must be considered
 * exposed: new keys go to the connection pages, and in `token` access both
 * tokens are to be regenerated, each here with the Access section's action
 * once the tokens have loaded.
 */
function ExposedSecretsNotice({ count }: { count: number }) {
  const { t } = useTranslation("service");
  const { t: tc } = useTranslation();
  const { accessMode } = panelSession();
  const { tokens, regenerate } = useAccessTokens();
  return (
    <Notice
      tone="danger"
      icon={AlertTriangle}
      title={t("security.exposed.title")}
    >
      <p>{t("security.exposed.detail", { count })}</p>
      <p>{t("security.exposed.keys")}</p>
      <div className="flex flex-wrap gap-1.5 py-1">
        <Button asChild variant="outline" size="sm">
          <Link to="/trading-connections">{tc("nav.tradingConnections")}</Link>
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link to="/market-data">{tc("nav.marketData")}</Link>
        </Button>
      </div>
      {accessMode === "token" && (
        <>
          <p>{t("security.exposed.tokens")}</p>
          {tokens.state === "ready" && (
            <div className="flex flex-wrap gap-x-4 gap-y-1.5 py-1">
              {(["agent", "api"] as const).map((kind) => (
                <div key={kind} className="flex items-center gap-2">
                  <span>{t(`security.tokens.${kind}.label`)}</span>
                  <RegenerateTokenButton
                    kind={kind}
                    onRegenerate={() => regenerate(kind)}
                  />
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </Notice>
  );
}

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
            <span>
              {storage.data.sealedAt === null
                ? t("security.storage.encryptionOn")
                : t("security.storage.sealed", {
                    date: formatDateTime(storage.data.sealedAt),
                  })}
            </span>
            <CompactStaleState load={storage} />
          </div>
        ) : (
          <div className="space-y-2">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="warn">
                <LockOpen aria-hidden="true" className="h-3 w-3" />
                {t("security.storage.noMasterKey")}
              </Badge>
              <span>{t("security.storage.plainText")}</span>
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
  let value: string | null = null;
  if (signing.state === "ready") {
    const active = signing.data.keys.find((key) => key.active);
    if (!signing.data.eSignEnabled) {
      value = t("security.storage.signingOff");
    } else if (active === undefined) {
      value = t("security.storage.signingWithoutKey");
    } else {
      value = t("security.storage.signingOn", { key: active.fingerprint });
    }
  }
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
      <p>
        {showsAccounts
          ? t("accountMode.modes.multi")
          : t("security.storage.oneAccount", { account })}
      </p>
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
      <CardContent className="space-y-3">
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
