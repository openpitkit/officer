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

import { useState } from "react";
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  ChevronsLeft,
  ChevronsRight,
  Download,
  File,
} from "lucide-react";
import { useTranslation } from "react-i18next";

import type {
  BusinessCsvEntity,
  BusinessCsvExportFilters,
} from "@/api/types";
import { BusinessCsvExportDialog } from "@/components/BusinessCsvDialogs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { useTablePageSizes } from "@/lib/tablePageSize";

export function TablePagination({
  canNext,
  canPrevious,
  knownTotalPages,
  onNext,
  onPage,
  onPrevious,
  page,
}: {
  canNext: boolean;
  canPrevious: boolean;
  knownTotalPages?: number;
  onNext: () => void;
  onPage?: (page: number) => void;
  onPrevious: () => void;
  page: number;
}) {
  const { t } = useTranslation("common");
  if (!canPrevious && !canNext) {
    return null;
  }
  const currentPage = page + 1;
  const hasKnownTotal = knownTotalPages !== undefined;
  const canGoToKnownLast =
    knownTotalPages !== undefined && currentPage < knownTotalPages;
  const visiblePageItems = (() => {
    if (knownTotalPages === undefined) {
      return [];
    }
    const total = knownTotalPages;
    const pages = new Set<number>([1, currentPage, total]);
    if (total <= 7) {
      for (let p = 1; p <= total; p += 1) {
        pages.add(p);
      }
    } else {
      for (let p = currentPage - 1; p <= currentPage + 1; p += 1) {
        if (p >= 1 && p <= total) {
          pages.add(p);
        }
      }
    }
    const sorted = Array.from(pages).sort((a, b) => a - b);
    return sorted.flatMap((visiblePage, index) => {
      const previous = sorted[index - 1];
      if (previous !== undefined && visiblePage - previous > 1) {
        return [`gap-${previous}-${visiblePage}`, visiblePage] as const;
      }
      return [visiblePage] as const;
    });
  })();
  const pageLabel =
    knownTotalPages === undefined
      ? t("table.pagination.pageOfUnknown", { page: currentPage })
      : t("table.pagination.pageOf", {
          page: currentPage,
          pages: knownTotalPages,
        });
  const jump = (nextPage: number) => {
    if (!onPage) {
      return;
    }
    const bounded =
      knownTotalPages === undefined
        ? Math.max(1, nextPage)
        : Math.min(Math.max(1, nextPage), knownTotalPages);
    onPage?.(bounded - 1);
  };
  return (
    <div className="flex flex-wrap items-center justify-end gap-1.5">
      <span className="px-1 text-xs text-muted-lt">{pageLabel}</span>
      {hasKnownTotal && onPage && (
        <Button
          type="button"
          variant="outline"
          size="icon"
          onClick={() => jump(1)}
          disabled={!canPrevious}
          aria-label={t("table.pagination.first")}
        >
          <ChevronsLeft className="h-3.5 w-3.5" />
        </Button>
      )}
      <Button
        type="button"
        variant="outline"
        size="icon"
        onClick={onPrevious}
        disabled={!canPrevious}
        aria-label={t("table.pagination.previous")}
      >
        <ChevronLeft className="h-3.5 w-3.5" />
      </Button>
      {hasKnownTotal &&
        onPage &&
        visiblePageItems.map((visiblePage) =>
          typeof visiblePage === "number" ? (
            <Button
              key={visiblePage}
              type="button"
              variant={visiblePage === currentPage ? "default" : "outline"}
              size="sm"
              className="min-w-8 px-2"
              onClick={() => jump(visiblePage)}
              aria-label={t("table.pagination.goToPage", {
                page: visiblePage,
              })}
            >
              {visiblePage}
            </Button>
          ) : (
            <span
              key={visiblePage}
              aria-hidden="true"
              className="px-1 text-xs text-muted-lt"
            >
              …
            </span>
          ),
        )}
      <Button
        type="button"
        variant="outline"
        size="icon"
        onClick={onNext}
        disabled={!canNext}
        aria-label={t("table.pagination.next")}
      >
        <ChevronRight className="h-3.5 w-3.5" />
      </Button>
      {hasKnownTotal && onPage && (
        <>
          <Button
            type="button"
            variant="outline"
            size="icon"
            onClick={() => jump(knownTotalPages)}
            disabled={!canGoToKnownLast}
            aria-label={t("table.pagination.last")}
          >
            <ChevronsRight className="h-3.5 w-3.5" />
          </Button>
          <Input
            type="number"
            min={1}
            max={knownTotalPages}
            className="h-8 w-14 text-xs"
            aria-label={t("table.pagination.goToPageInput")}
            placeholder={t("table.pagination.goToPagePlaceholder")}
            onKeyDown={(event) => {
              if (event.key !== "Enter") {
                return;
              }
              const value = Number(event.currentTarget.value);
              if (Number.isInteger(value) && value > 0) {
                jump(value);
              }
              event.currentTarget.value = "";
            }}
          />
        </>
      )}
    </div>
  );
}

export function PageSizeSelect({
  ariaLabel,
  onChange,
  rowCountLabel,
  value,
}: {
  ariaLabel: string;
  onChange: (value: number) => void;
  rowCountLabel: (count: number) => string;
  value: number;
}) {
  const pageSizes = useTablePageSizes();
  return (
    <Select value={String(value)} onValueChange={(v) => onChange(Number(v))}>
      <SelectTrigger
        className="h-8 w-16 text-xs tabular-nums"
        aria-label={ariaLabel}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {pageSizes.map((n) => (
          <SelectItem
            key={n}
            value={String(n)}
            aria-label={rowCountLabel(n)}
          >
            {n}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

export function CsvTransferMenu({
  exports,
}: {
  exports?: {
    entity: BusinessCsvEntity;
    filters?: BusinessCsvExportFilters;
    label: string;
  }[];
}) {
  const { t } = useTranslation("common");
  const exportItems = exports ?? [];
  const [exportIndex, setExportIndex] = useState<number | null>(null);
  if (exportItems.length === 0) {
    return null;
  }
  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button
            variant="outline"
            size="sm"
            aria-label={t("businessCsv.menu.ariaLabel")}
          >
            <File className="h-3.5 w-3.5" />
            {t("businessCsv.menu.trigger")}
            <ChevronDown className="h-3.5 w-3.5" />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {exportItems.map((item, index) => (
            <DropdownMenuItem
              key={`${item.entity}-${item.label}`}
              onSelect={() => setExportIndex(index)}
            >
              <Download className="h-3.5 w-3.5" />
              {item.label}
            </DropdownMenuItem>
          ))}
        </DropdownMenuContent>
      </DropdownMenu>
      {exportItems.map((item, index) => (
        <BusinessCsvExportDialog
          key={`${item.entity}-${item.label}`}
          entity={item.entity}
          filters={item.filters}
          open={exportIndex === index}
          onOpenChange={(open) => setExportIndex(open ? index : null)}
          trigger={() => null}
        />
      ))}
    </>
  );
}
