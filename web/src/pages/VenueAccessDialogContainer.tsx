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

import { type ReactNode, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";

import type { TradingAccessSubject, TradingSnapshot } from "@/api/types";
import { useAccountState } from "@/api/useAccounts";
import type { PollingResult } from "@/api/usePolling";
import { ErrorBanner, StaleState } from "@/components/PageStates";
import {
  accessGrantBlock,
  RevokeAccessDialog,
  VenueAccessDialog,
  type VenueAccessDialogRow,
} from "@/components/venues";
import { useOfficerApi } from "@/framework";

/** Router path of the page that sets up trading connections, their fee
 *  accounts and venue accounts. */
const TRADING_CONNECTIONS_PATH = "/trading-connections";

/** VenueAccessDialog for `subject` over the page's trading snapshot: grants
 *  the subject's own grant, revokes it once confirmed (never a venue account,
 *  which stays on the connection page), then reloads the snapshot. When
 *  `gone`, the subject no longer exists and the dialog closes, but never
 *  under a change in flight or over its error: how the change ended is shown
 *  first. */
function AccessDialog({
  subject,
  group,
  gone,
  trading,
  onClose,
  children,
}: {
  subject: TradingAccessSubject;
  /** The account's group, as VenueAccessDialog reads it; null for a group
   *  subject. */
  group: string | null;
  gone: boolean;
  trading: PollingResult<TradingSnapshot>;
  onClose: () => void;
  /** Notices about the subject itself, shown above the rows. */
  children?: ReactNode;
}) {
  const { t } = useTranslation("trading");
  const api = useOfficerApi();
  const [busy, setBusy] = useState(false);
  const [changeError, setChangeError] = useState<string>();
  // Connection whose grant the operator asked to revoke, until confirmed or
  // cancelled.
  const [revokeId, setRevokeId] = useState<string | null>(null);

  useEffect(() => {
    if (gone && !busy && changeError === undefined) onClose();
  }, [gone, busy, changeError, onClose]);

  let connections: VenueAccessDialogRow[] | null = null;
  let loadError: string | undefined;
  const { load } = trading;
  if (load.state === "error") {
    loadError = load.error;
  } else if (load.state === "ready") {
    connections = load.data.connections.map(
      (connection): VenueAccessDialogRow => {
        const row = {
          id: connection.id,
          label: connection.label,
          providerType: connection.provider,
          modeLabel: t(`mode.${connection.mode}`),
        };
        const provider = load.data.providers.find(
          (item) => item.type === connection.provider,
        );
        if (provider === undefined) {
          return {
            ...row,
            unavailable: t("missingProvider", {
              provider: connection.provider,
            }),
          };
        }
        const ownGrant =
          connection.access.find((grant) =>
            subject.accountGroup !== undefined
              ? grant.accountGroup === subject.accountGroup
              : grant.account === subject.account,
          ) ?? null;
        return {
          ...row,
          providerTitle: provider.title,
          ownGrant,
          groupGrant:
            group !== null && group !== ""
              ? (connection.access.find(
                  (grant) => grant.accountGroup === group,
                ) ?? null)
              : null,
          grantBlock:
            ownGrant === null
              ? accessGrantBlock(provider, connection, subject)
              : null,
        };
      },
    );
  }
  // The row to confirm a revoke on: its own grant must still be one the
  // dialog offers to revoke, unbound to a venue account. A poll that binds
  // it, drops it or drops the row closes the confirmation for good. While
  // open, the confirmation shows how its revoke ended in place of the
  // dialog.
  const revokeRow =
    revokeId === null
      ? undefined
      : connections?.find(
          (row) => row.id === revokeId && row.ownGrant?.venueAccount === "",
        );
  if (revokeId !== null && revokeRow === undefined) {
    setRevokeId(null);
  }
  const dialogError =
    loadError ?? (revokeRow === undefined ? changeError : undefined);

  const change = async (request: () => Promise<unknown>) => {
    setBusy(true);
    setChangeError(undefined);
    try {
      await request();
      trading.reload();
    } catch (err) {
      setChangeError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <VenueAccessDialog
        open
        subject={subject}
        group={group}
        connections={connections}
        connectionsHref={TRADING_CONNECTIONS_PATH}
        busy={busy}
        error={dialogError}
        onOpenChange={(next) => {
          if (!next) onClose();
        }}
        onGrant={(connectionId) =>
          void change(() => api.grantTradingAccess(connectionId, subject))
        }
        onRevoke={(connectionId) => {
          setChangeError(undefined);
          setRevokeId(connectionId);
        }}
      >
        {children}
        {/* A failed background poll keeps the last rows; say they are stale. */}
        <StaleState load={load} reload={trading.reload} />
      </VenueAccessDialog>
      {revokeRow !== undefined && (
        <RevokeAccessDialog
          open
          subject={subject}
          connectionLabel={revokeRow.label}
          busy={busy}
          error={changeError}
          onOpenChange={(next) => {
            if (!next) setRevokeId(null);
          }}
          onConfirm={() =>
            void change(async () => {
              await api.revokeTradingAccess(revokeRow.id, subject);
              setRevokeId(null);
            })
          }
        />
      )}
    </>
  );
}

/** The dialog for one account, whose group it polls itself: the group the
 *  page listed may be stale, and the account may leave the listed page
 *  without leaving Officer. Until the group is known the rows claim no
 *  access through it. */
function AccountAccessDialog({
  code,
  trading,
  onClose,
}: {
  code: string;
  trading: PollingResult<TradingSnapshot>;
  onClose: () => void;
}) {
  const { t } = useTranslation("accounts");
  const accountState = useAccountState(code);
  const { load } = accountState;
  return (
    <AccessDialog
      subject={{ account: code }}
      group={
        load.state === "ready" && load.data !== null
          ? load.data.account.group
          : null
      }
      gone={load.state === "ready" && load.data === null}
      trading={trading}
      onClose={onClose}
    >
      {load.state === "error" && (
        <ErrorBanner
          message={t("venueAccessGroupCheckFailed", { error: load.error })}
        />
      )}
      <StaleState load={load} reload={accountState.reload} />
    </AccessDialog>
  );
}

/** Open VenueAccessDialog for an account or a group, by identity only: an
 *  account's group is polled while the dialog is open. */
export function VenueAccessDialogContainer({
  subject,
  trading,
  onClose,
}: {
  subject: TradingAccessSubject;
  trading: PollingResult<TradingSnapshot>;
  onClose: () => void;
}) {
  return subject.accountGroup !== undefined ? (
    <AccessDialog
      subject={subject}
      group={null}
      gone={false}
      trading={trading}
      onClose={onClose}
    />
  ) : (
    <AccountAccessDialog
      code={subject.account}
      trading={trading}
      onClose={onClose}
    />
  );
}
