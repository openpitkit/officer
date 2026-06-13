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

import { useCallback, useEffect, useRef, useState } from "react";

/** Async load lifecycle for a value of type T. */
export type LoadState<T> =
  | { state: "loading"; data: null; error: null }
  | { state: "ready"; data: T; error: null }
  | { state: "error"; data: null; error: string };

export interface PollingResult<T> {
  load: LoadState<T>;
  /** Re-fetch now, resetting to loading and clearing any prior error. */
  reload: () => void;
}

/**
 * Fetch `fetcher` on mount and then on a fixed interval, exposing
 * loading / ready / error states. The fetcher must accept an AbortSignal so an
 * unmount or re-fetch cancels the in-flight request. Background polls keep the
 * last good data visible (no loading flash); `reload` shows loading again.
 */
export function usePolling<T>(
  fetcher: (signal: AbortSignal) => Promise<T>,
  intervalMs = 5000,
): PollingResult<T> {
  const [load, setLoad] = useState<LoadState<T>>({
    state: "loading",
    data: null,
    error: null,
  });
  const [nonce, setNonce] = useState(0);

  // Hold the latest fetcher so the polling effect does not restart when an
  // inline closure identity changes between renders. The render-phase write
  // is intentional: the running interval must call the current fetcher
  // without re-subscribing, so the ref cannot lag behind in an effect.
  const fetcherRef = useRef(fetcher);
  // eslint-disable-next-line react-hooks/refs
  fetcherRef.current = fetcher;

  const reload = useCallback(() => {
    setLoad({ state: "loading", data: null, error: null });
    setNonce((n) => n + 1);
  }, []);

  useEffect(() => {
    let active = true;
    // Latest controller, for the next run's pre-abort and for cleanup.
    let current: AbortController | null = null;

    const run = () => {
      current?.abort();
      // Capture this run's controller locally so a late rejection checks its
      // own signal, not a controller a later run has since replaced.
      const controller = new AbortController();
      current = controller;
      fetcherRef
        .current(controller.signal)
        .then((data) => {
          if (active && !controller.signal.aborted) {
            setLoad({ state: "ready", data, error: null });
          }
        })
        .catch((err: unknown) => {
          if (!active || controller.signal.aborted) {
            return;
          }
          const message = err instanceof Error ? err.message : String(err);
          setLoad((prev) =>
            // Keep showing prior data on a background failure; surface the
            // error only when nothing has loaded yet.
            prev.state === "ready"
              ? prev
              : { state: "error", data: null, error: message },
          );
        });
    };

    run();
    const timer = window.setInterval(run, intervalMs);

    return () => {
      active = false;
      current?.abort();
      window.clearInterval(timer);
    };
  }, [nonce, intervalMs]);

  return { load, reload };
}
