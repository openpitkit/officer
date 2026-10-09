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

import { useRef, useState } from "react";
import type { FailedVerificationOutcome } from "./VerifyResult";

/** Null responses and thrown errors produce a failure outcome. */
export type RowVerificationOutcome<Outcome> =
  | Outcome
  | FailedVerificationOutcome;

/** Independent row verification state, keyed by external symbol. */
export interface RowVerificationState<Outcome> {
  outcomes: Record<string, RowVerificationOutcome<Outcome>>;
  isVerifying: (symbol: string) => boolean;
  verifyRow: (symbol: string) => Promise<void>;
  /** Clears the verdict and discards responses from pending requests. */
  forget: (symbol: string) => void;
}

/** Keeps all pending rows disabled and only the latest verdict per symbol. */
export function useRowVerification<Outcome>(
  verify: (symbol: string) => Promise<Outcome | null>,
): RowVerificationState<Outcome> {
  const [outcomes, setOutcomes] = useState<
    Record<string, RowVerificationOutcome<Outcome>>
  >({});
  const [pendingSymbols, setPendingSymbols] = useState<Set<string>>(new Set());
  const requests = useRef(
    new Map<string, { latest: number; pending: number }>(),
  );

  const verifyRow = async (symbol: string) => {
    let state = requests.current.get(symbol);
    if (state === undefined) {
      state = { latest: 0, pending: 0 };
      requests.current.set(symbol, state);
    }
    const request = ++state.latest;
    state.pending++;
    setPendingSymbols((previous) => new Set(previous).add(symbol));
    try {
      const result = await verify(symbol);
      if (request === state.latest) {
        setOutcomes((previous) => ({
          ...previous,
          [symbol]: result === null ? { failed: true } : result,
        }));
      }
    } catch (err) {
      if (request === state.latest) {
        setOutcomes((previous) => ({
          ...previous,
          [symbol]: {
            failed: true,
            message: err instanceof Error ? err.message : String(err),
          },
        }));
      }
    } finally {
      state.pending--;
      if (state.pending === 0) {
        setPendingSymbols((previous) => {
          const next = new Set(previous);
          next.delete(symbol);
          return next;
        });
      }
    }
  };

  const forget = (symbol: string) => {
    const state = requests.current.get(symbol);
    if (state !== undefined) state.latest++;
    setOutcomes((previous) => {
      const next = { ...previous };
      delete next[symbol];
      return next;
    });
  };

  return {
    outcomes,
    isVerifying: (symbol) => pendingSymbols.has(symbol),
    verifyRow,
    forget,
  };
}
