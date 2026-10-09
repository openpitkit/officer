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

import { SearchCheck, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { useTranslation } from "react-i18next";
import {
  Table,
  TableHeader,
  TableBody,
  TableRow,
  TableHead,
  TableCell,
} from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

/** Minimum instrument identity and state required by the table. */
export interface InstrumentTableRow {
  externalSymbol: string;
  baseAsset: string;
  quoteAsset: string;
  enabled: boolean;
}

/** Extra columns appear between the instrument pair and state. */
export interface InstrumentsTableColumn<Instrument extends InstrumentTableRow> {
  /** Unique, stable identity within the table, preserved when columns reorder. */
  id: string;
  header: string;
  renderCell: (instrument: Instrument) => ReactNode;
  /** Omitted uses the standard cell spacing. */
  cellClassName?: string;
}

/** Controlled rows and actions with optional context-specific adornments. */
export interface InstrumentsTableProps<Instrument extends InstrumentTableRow> {
  instruments: Instrument[];
  busy: boolean;
  ariaLabel?: string;
  /** Omitted displays externalSymbol. */
  displaySymbol?: (instrument: Instrument) => string;
  metadata?: (instrument: Instrument) => string;
  pairAdornment?: (instrument: Instrument) => ReactNode;
  extraColumns?: InstrumentsTableColumn<Instrument>[];
  parentEnabled: boolean;
  parentDisabledLabel: string;
  stateAdornment?: (instrument: Instrument) => ReactNode;
  onToggle: (instrument: Instrument) => void;
  onVerify?: (instrument: Instrument) => void;
  isVerifying?: (symbol: string) => boolean;
  onDelete: (instrument: Instrument) => void;
}

/** Renders instrument state and actions without owning application data. */
export function InstrumentsTable<Instrument extends InstrumentTableRow>({
  instruments,
  busy,
  ariaLabel,
  displaySymbol,
  metadata,
  pairAdornment,
  extraColumns,
  parentEnabled,
  parentDisabledLabel,
  stateAdornment,
  onToggle,
  onVerify,
  isVerifying,
  onDelete,
}: InstrumentsTableProps<Instrument>) {
  const { t } = useTranslation("venues");
  return (
    <Table aria-label={ariaLabel}>
      <TableHeader>
        <TableRow>
          <TableHead>{t("table.external")}</TableHead>
          <TableHead>{t("table.instrument")}</TableHead>
          {extraColumns?.map((column) => (
            <TableHead key={column.id}>{column.header}</TableHead>
          ))}
          <TableHead>{t("table.state")}</TableHead>
          <TableHead className="text-right">{t("table.actions")}</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {instruments.length === 0 && (
          <TableRow>
            <TableCell
              className="py-5 text-center text-muted"
              colSpan={4 + (extraColumns?.length ?? 0)}
            >
              {t("emptyInstruments")}
            </TableCell>
          </TableRow>
        )}
        {instruments.map((instrument) => {
          const rowMetadata = metadata?.(instrument);
          return (
            <TableRow key={instrument.externalSymbol}>
              <TableCell className="font-medium">
                <div>
                  {displaySymbol
                    ? displaySymbol(instrument)
                    : instrument.externalSymbol}
                </div>
                {rowMetadata && (
                  <div className="mt-0.5 text-xs font-normal text-muted">
                    {rowMetadata}
                  </div>
                )}
              </TableCell>
              <TableCell className="text-muted">
                <div className="flex items-center gap-1.5">
                  <span>
                    {instrument.baseAsset}/{instrument.quoteAsset}
                  </span>
                  {pairAdornment?.(instrument)}
                </div>
              </TableCell>
              {extraColumns?.map((column) => (
                <TableCell key={column.id} className={column.cellClassName}>
                  {column.renderCell(instrument)}
                </TableCell>
              ))}
              <TableCell>
                <div className="flex flex-wrap items-center gap-2">
                  <Badge
                    variant={
                      instrument.enabled && parentEnabled ? "ok" : "neutral"
                    }
                  >
                    {!instrument.enabled
                      ? t("state.disabled")
                      : parentEnabled
                        ? t("state.enabled")
                        : parentDisabledLabel}
                  </Badge>
                  {stateAdornment?.(instrument)}
                </div>
              </TableCell>
              <TableCell>
                <div className="flex justify-end gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => onToggle(instrument)}
                    disabled={busy}
                  >
                    {instrument.enabled
                      ? t("actions.disable")
                      : t("actions.enable")}
                  </Button>
                  {onVerify && (
                    <Button
                      type="button"
                      variant="ghost"
                      size="icon"
                      aria-label={t("verify.action")}
                      onClick={() => onVerify(instrument)}
                      disabled={
                        busy || isVerifying?.(instrument.externalSymbol)
                      }
                    >
                      <SearchCheck />
                    </Button>
                  )}
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon"
                    aria-label={t("actions.deleteInstrument")}
                    onClick={() => onDelete(instrument)}
                    disabled={busy}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}
