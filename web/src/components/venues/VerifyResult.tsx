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

import { Check, TriangleAlert, X } from "lucide-react";
import { useTranslation } from "react-i18next";

/** Explicit failure of a verification request. */
export type FailedVerificationOutcome = { failed: true; message?: string };

/** A verification verdict or an explicit request failure. */
export type SymbolVerificationOutcome =
  | {
      supported: boolean;
      exists: boolean;
      tradable?: boolean;
      suggestion?: string;
      details?: string;
    }
  | FailedVerificationOutcome;

/** Context-specific unavailability wording is optional. */
export interface VerifyResultProps {
  result: SymbolVerificationOutcome | null;
  /** Used for a missing symbol with details; omitted means plain not-found. */
  unavailableLabel?: string;
  /** Muted unsupported verdict; omitted leaves unsupported results hidden. */
  unsupportedLabel?: string;
}

/** Renders supported verdicts, request failures, and non-tradable warnings. */
export function VerifyResult({
  result,
  unavailableLabel,
  unsupportedLabel,
}: VerifyResultProps) {
  const { t } = useTranslation("venues");
  if (!result) return null;
  if ("failed" in result) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--danger)]">
        <X className="h-3 w-3" />
        {t("verify.failed")}
      </span>
    );
  }
  if (!result.supported) {
    return unsupportedLabel ? (
      <span className="text-xs text-muted">{unsupportedLabel}</span>
    ) : null;
  }
  if (result.exists && result.tradable === false) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--warn)]">
        <TriangleAlert className="h-3 w-3" />
        {t("verify.notTradable")}
        {result.details && (
          <span className="text-muted">({result.details})</span>
        )}
      </span>
    );
  }
  if (result.exists) {
    return (
      <span className="inline-flex flex-wrap items-center gap-1 text-xs">
        <span className="inline-flex items-center gap-1 text-[var(--ok)]">
          <Check className="h-3 w-3" />
          {t("verify.exists")}
        </span>
        {result.details && (
          <span className="text-muted">({result.details})</span>
        )}
      </span>
    );
  }
  return (
    <span className="inline-flex flex-wrap items-center gap-1 text-xs text-[var(--danger)]">
      <X className="h-3 w-3" />
      {result.details && unavailableLabel
        ? unavailableLabel
        : t("verify.notFound")}
      {result.suggestion && (
        <span className="text-muted">
          {t("verify.didYouMean", { suggestion: result.suggestion })}
        </span>
      )}
      {result.details && <span className="text-muted">({result.details})</span>}
    </span>
  );
}
