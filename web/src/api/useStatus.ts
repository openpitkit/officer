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

import { useCallback, useEffect, useState } from "react";

import { fetchStatus } from "@/api/client";
import type { Status } from "@/api/types";

/** Async load lifecycle for a value of type T. */
export type LoadState<T> =
  | { state: "loading"; data: null; error: null }
  | { state: "ready"; data: T; error: null }
  | { state: "error"; data: null; error: string };

interface UseStatusResult {
  status: LoadState<Status>;
  /** Re-fetch the status; clears any prior error and shows loading. */
  reload: () => void;
}

/** Fetch GET /api/v1/status on mount, exposing loading / ready / error
 *  states. */
export function useStatus(): UseStatusResult {
  const [status, setStatus] = useState<LoadState<Status>>({
    state: "loading",
    data: null,
    error: null,
  });
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => {
    setStatus({ state: "loading", data: null, error: null });
    setNonce((n) => n + 1);
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    let active = true;

    fetchStatus(controller.signal)
      .then((data) => {
        if (active) {
          setStatus({ state: "ready", data, error: null });
        }
      })
      .catch((err: unknown) => {
        if (!active || controller.signal.aborted) {
          return;
        }
        const message = err instanceof Error ? err.message : String(err);
        setStatus({ state: "error", data: null, error: message });
      });

    return () => {
      active = false;
      controller.abort();
    };
  }, [nonce]);

  return { status, reload };
}
