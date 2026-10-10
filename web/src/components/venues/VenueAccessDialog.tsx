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

import type { TFunction } from "i18next";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import type { TradingAccessGrant, TradingAccessSubject } from "@/api/types";
import {
  EmptyState,
  ErrorBanner,
  TableSkeleton,
} from "@/components/PageStates";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { AccessBlockedHint } from "./AccessBlockedHint";
import type { AccessGrantBlock } from "./accessGrantBlock";
import { LinkButton } from "./LinkButton";
import { ProviderIcon } from "./ProviderIcon";
import { WarnHint } from "./WarnHint";

/** One trading connection as it stands for the dialog's subject: editable,
 *  or `unavailable` with the reason it cannot be edited here. */
export type VenueAccessDialogRow = {
  /** Connection id, passed back by `onGrant` and `onRevoke`. */
  id: string;
  label: string;
  /** Provider type, for its icon. */
  providerType: string;
  /** Display label of the connection mode. */
  modeLabel: string;
} & (
  | {
      /** Provider title, named by the blocked reason. */
      providerTitle: string;
      /** The subject's own grant on the connection; null when it holds none.
       *  One bound to a venue account is not revoked here: the dialog cannot
       *  grant it back with that binding. */
      ownGrant: TradingAccessGrant | null;
      /** The grant of the account's group on the connection; null when the
       *  group holds none. Not read for a group subject or while the
       *  account's group is not known. */
      groupGrant: TradingAccessGrant | null;
      /** Why granting the subject would be refused; null when nothing blocks
       *  it. Read only while the subject holds no own grant. */
      grantBlock: AccessGrantBlock | null;
      unavailable?: never;
    }
  | {
      /** Why the connection cannot be edited here, shown in place of its
       *  access; the row offers no action. */
      unavailable: string;
      providerTitle?: never;
      ownGrant?: never;
      groupGrant?: never;
      grantBlock?: never;
    }
);

/** A row the subject's access can be edited on. */
export type VenueAccessDialogEditableRow = Extract<
  VenueAccessDialogRow,
  { unavailable?: never }
>;

/** Controlled editor of one account's or group's own grants, with
 *  caller-supplied rows, busy state and error; the caller performs every
 *  grant, and confirms and performs every revoke. */
export interface VenueAccessDialogProps {
  open: boolean;
  /** The account or group whose access the dialog edits. */
  subject: TradingAccessSubject;
  /** The account's group code, empty for an account outside any group, and
   *  null while its membership is not known: the rows then claim no access
   *  through a group and granting waits, since an own grant may replace a
   *  group's venue account. Read only for an account subject. */
  group: string | null;
  /** One row per trading connection; null while the connections are not
   *  known. */
  connections: VenueAccessDialogRow[] | null;
  /** In-app router path of the page that sets up trading connections, their
   *  fee accounts and venue accounts, followed client-side, so a router must
   *  be above. */
  connectionsHref: string;
  /** Disables granting, revoking and closing while a change is in flight. */
  busy: boolean;
  error?: string;
  /** Notices shown above the rows, such as a stale-data notice. */
  children?: ReactNode;
  onOpenChange: (open: boolean) => void;
  onGrant: (connectionId: string) => void;
  onRevoke: (connectionId: string) => void;
}

function accessState(
  t: TFunction<"venues">,
  subject: TradingAccessSubject,
  group: string | null,
  row: VenueAccessDialogEditableRow,
): string {
  if (subject.accountGroup !== undefined) {
    return t(
      row.ownGrant !== null
        ? "accessDialog.state.granted"
        : "accessDialog.state.none",
    );
  }
  if (group === null) {
    // Unknown membership: only the own grant is known.
    return t(
      row.ownGrant !== null
        ? "accessDialog.state.own"
        : "accessDialog.state.noOwn",
    );
  }
  if (row.groupGrant === null) {
    return t(
      row.ownGrant !== null
        ? "accessDialog.state.own"
        : "accessDialog.state.none",
    );
  }
  return t(
    row.ownGrant !== null
      ? "accessDialog.state.ownAndGroup"
      : "accessDialog.state.group",
    { group },
  );
}

/** Access and action cells of an editable row. A venue account binding is
 *  left to the connection page: revoking an own grant bound to one would
 *  drop it, and an own grant would replace the one the group's grant binds. */
function AccessCells({
  subject,
  group,
  row,
  connectionsHref,
  busy,
  onGrant,
  onRevoke,
}: {
  subject: TradingAccessSubject;
  group: string | null;
  row: VenueAccessDialogEditableRow;
  connectionsHref: string;
  busy: boolean;
  onGrant: (connectionId: string) => void;
  onRevoke: (connectionId: string) => void;
}) {
  const { t } = useTranslation("venues");
  const isAccount = subject.account !== undefined;
  const membershipUnknown = isAccount && group === null;
  const groupVenueAccount =
    isAccount &&
    !membershipUnknown &&
    row.ownGrant === null &&
    row.groupGrant !== null
      ? row.groupGrant.venueAccount
      : "";
  return (
    <>
      <TableCell className="space-y-1 align-top text-xs">
        <p>{accessState(t, subject, group, row)}</p>
        {row.ownGrant !== null && row.ownGrant.venueAccount !== "" && (
          <>
            <p className="text-muted">
              {t("accessDialog.venueAccount", {
                venueAccount: row.ownGrant.venueAccount,
              })}
            </p>
            <p className="text-muted">{t("accessDialog.venueAccountBound")}</p>
            <LinkButton to={connectionsHref}>
              {t("accessDialog.openConnections")}
            </LinkButton>
          </>
        )}
        {groupVenueAccount !== "" && (
          <WarnHint
            reason={t("accessDialog.groupVenueAccount", {
              venueAccount: groupVenueAccount,
            })}
            to={connectionsHref}
          >
            {t("accessDialog.openConnections")}
          </WarnHint>
        )}
        {row.ownGrant === null && row.grantBlock !== null && (
          <AccessBlockedHint
            reason={row.grantBlock}
            providerTitle={row.providerTitle}
            feeAccountHref={connectionsHref}
          />
        )}
      </TableCell>
      <TableCell className="text-right align-top">
        {row.ownGrant === null ? (
          <Button
            size="sm"
            disabled={
              busy ||
              membershipUnknown ||
              row.grantBlock !== null ||
              groupVenueAccount !== ""
            }
            onClick={() => onGrant(row.id)}
          >
            {t("actions.grantAccess")}
          </Button>
        ) : (
          row.ownGrant.venueAccount === "" && (
            <Button
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => onRevoke(row.id)}
            >
              {t("actions.revokeAccess")}
            </Button>
          )
        )}
      </TableCell>
    </>
  );
}

/** Lists every trading connection with the subject's access to it and grants
 *  or revokes the subject's own grant from the same row. Revoking never
 *  touches an account's access through its group. */
export function VenueAccessDialog({
  open,
  subject,
  group,
  connections,
  connectionsHref,
  busy,
  error,
  children,
  onOpenChange,
  onGrant,
  onRevoke,
}: VenueAccessDialogProps) {
  const { t } = useTranslation("venues");
  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!busy) onOpenChange(next);
      }}
    >
      <DialogContent className="max-h-[90vh] max-w-2xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>
            {subject.accountGroup !== undefined
              ? t("accessDialog.groupTitle", { code: subject.accountGroup })
              : t("accessDialog.accountTitle", { code: subject.account })}
          </DialogTitle>
          <DialogDescription>
            {t(
              subject.accountGroup !== undefined
                ? "accessDialog.groupDescription"
                : "accessDialog.accountDescription",
            )}
          </DialogDescription>
        </DialogHeader>
        {error && <ErrorBanner message={error} />}
        {children}
        {connections === null ? (
          !error && <TableSkeleton rows={2} cols={3} />
        ) : connections.length === 0 ? (
          <EmptyState
            title={t("emptyConnections")}
            hint={t("accessDialog.empty.hint")}
            action={
              <Button asChild variant="outline" size="sm">
                <Link to={connectionsHref}>
                  {t("common:actions.setUpTradingConnection")}
                </Link>
              </Button>
            }
          />
        ) : (
          <Table aria-label={t("accessDialog.tableLabel")}>
            <TableHeader>
              <TableRow>
                <TableHead>{t("accessDialog.columns.connection")}</TableHead>
                <TableHead>{t("accessDialog.columns.access")}</TableHead>
                <TableHead className="text-right">
                  {t("table.actions")}
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {connections.map((row) => (
                <TableRow key={row.id}>
                  <TableCell className="align-top">
                    <div className="flex items-center gap-2">
                      <ProviderIcon
                        type={row.providerType}
                        className="h-4 w-4 shrink-0"
                      />
                      <span className="font-medium">{row.label}</span>
                      <Badge>{row.modeLabel}</Badge>
                    </div>
                  </TableCell>
                  {row.unavailable !== undefined ? (
                    <TableCell colSpan={2} className="align-top">
                      <ErrorBanner message={row.unavailable} />
                    </TableCell>
                  ) : (
                    <AccessCells
                      subject={subject}
                      group={group}
                      row={row}
                      connectionsHref={connectionsHref}
                      busy={busy}
                      onGrant={onGrant}
                      onRevoke={onRevoke}
                    />
                  )}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </DialogContent>
    </Dialog>
  );
}
