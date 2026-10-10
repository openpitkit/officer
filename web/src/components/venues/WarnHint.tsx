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

import { LinkButton, type LinkButtonProps } from "./LinkButton";

/** A warning `reason` plus the LinkButton that acts on it; `children` label
 *  the action. */
export type WarnHintProps = { reason: string } & LinkButtonProps;

/** States a warning, then offers the one way to act on it. */
export function WarnHint({ reason, ...action }: WarnHintProps) {
  return (
    <div className="space-y-2 text-xs text-[var(--warn)]">
      <p>{reason}</p>
      <LinkButton {...action} />
    </div>
  );
}
