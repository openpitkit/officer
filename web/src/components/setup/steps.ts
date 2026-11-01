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

import { SETUP_STEPS } from "@/api/setup";

const [welcome, accounts, ...afterAccounts] = SETUP_STEPS;

/**
 * The wizard's steps in order: the five the server stores, with the funds
 * check after the accounts step, then two more. The server stores the
 * position only at its own steps: not at the funds check, whose decision is
 * the stored funds limit mode, nor at the two steps after completion.
 */
export const WIZARD_STEPS = [
  welcome,
  accounts,
  "funds",
  ...afterAccounts,
  "agent",
  "done",
] as const;

export type WizardStep = (typeof WIZARD_STEPS)[number];

/** The steps whose decision the summary lists, with where each one lives. */
export type DecisionStep = Exclude<WizardStep, "welcome" | "done">;
