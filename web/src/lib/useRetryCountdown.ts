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

import { useCallback, useEffect, useState } from "react";

/**
 * The seconds left before the server accepts the next attempt, down to 0, and
 * the function that starts a countdown.
 *
 * The count comes from an absolute deadline, so a timer that fires late - a
 * frozen tab, a sleeping machine - still shows the true remainder; the timer
 * only refreshes the display, once a second, until the deadline is reached,
 * whatever it showed last.
 */
export function useRetryCountdown(): [number, (seconds: number) => void] {
  const [deadline, setDeadline] = useState<number | null>(null);
  const [seconds, setSeconds] = useState(0);
  useEffect(() => {
    if (deadline === null) {
      return;
    }
    const timer = window.setInterval(() => {
      const left = Math.max(0, Math.ceil((deadline - Date.now()) / 1000));
      setSeconds(left);
      if (left === 0) {
        window.clearInterval(timer);
        setDeadline(null);
      }
    }, 1000);
    return () => window.clearInterval(timer);
  }, [deadline]);
  const start = useCallback((next: number) => {
    setDeadline(Date.now() + next * 1000);
    setSeconds(next);
  }, []);
  return [seconds, start];
}
