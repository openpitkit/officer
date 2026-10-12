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

/**
 * Post-copy flash state: `markCopied` raises `copied`, which falls back to
 * `false` after `resetMs`. The pending reset is cancelled on unmount.
 */
export function useCopyFeedback(resetMs: number) {
  const [copied, setCopied] = useState(false);

  useEffect(() => {
    if (!copied) {
      return undefined;
    }
    const timeout = window.setTimeout(() => setCopied(false), resetMs);
    return () => window.clearTimeout(timeout);
  }, [copied, resetMs]);

  const markCopied = useCallback(() => setCopied(true), []);
  return [copied, markCopied] as const;
}
