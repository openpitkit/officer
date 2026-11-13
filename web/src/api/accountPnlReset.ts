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

import { invalid, isRecord } from "@/api/decode";
import type { ApiClient } from "@/framework";

export interface ResetTermination {
  mode: "forever" | "count" | "until";
  maxRuns?: number;
  until?: string;
}
export interface ResetScheduleInput {
  enabled: boolean;
  time: string;
  timezone: string;
  termination: ResetTermination;
  revision: number;
}
export interface ResetScheduleJob {
  id: string;
  state: string;
  revision: number;
  completedRuns: number;
  error?: string;
  spec: {
    schedule: { time: string; timezone: string };
    termination: ResetTermination;
  };
  next: { at: string };
  last?: {
    finishedAt: string;
    outcome: string;
    reason?: string;
    error?: string;
    result?: {
      skippedReason: string;
      accounts: { account: string; error: string }[];
    };
  };
}
export interface ResetScheduleState {
  job: ResetScheduleJob | null;
  runtimeError: string;
}
const RESPONSE = "P&L reset schedule";
const states = [
  "ready",
  "paused",
  "running",
  "failed",
  "interrupted",
  "completed",
];
function text(v: unknown, field: string): string {
  if (typeof v !== "string") throw invalid(RESPONSE, field, "must be a string");
  return v;
}
function integer(v: unknown, field: string): number {
  if (typeof v !== "number" || !Number.isSafeInteger(v) || v < 0)
    throw invalid(RESPONSE, field, "must be a non-negative integer");
  return v;
}
export function decodeResetSchedule(value: unknown): ResetScheduleState {
  if (!isRecord(value)) throw invalid(RESPONSE, "body", "must be an object");
  const runtimeError = text(value.runtimeError, "runtimeError");
  if (value.job === null) return { job: null, runtimeError };
  const j = value.job;
  if (
    !isRecord(j) ||
    !isRecord(j.spec) ||
    !isRecord(j.spec.schedule) ||
    !isRecord(j.spec.termination) ||
    !isRecord(j.next)
  )
    throw invalid(RESPONSE, "job", "must be a job");
  const mode = j.spec.termination.mode;
  if (mode !== "forever" && mode !== "count" && mode !== "until")
    throw invalid(RESPONSE, "termination.mode", "is unknown");
  const termination: ResetTermination = { mode };
  if (mode === "count")
    termination.maxRuns = integer(j.spec.termination.maxRuns, "maxRuns");
  if (mode === "until")
    termination.until = text(j.spec.termination.until, "until");
  const state = text(j.state, "state");
  if (!states.includes(state)) throw invalid(RESPONSE, "state", "is unknown");
  const job: ResetScheduleJob = {
    id: text(j.id, "id"),
    state,
    revision: integer(j.revision, "revision"),
    completedRuns: integer(j.completedRuns, "completedRuns"),
    spec: {
      schedule: {
        time: text(j.spec.schedule.time, "time"),
        timezone: text(j.spec.schedule.timezone, "timezone"),
      },
      termination,
    },
    next: { at: text(j.next.at, "next.at") },
  };
  if (j.error !== undefined) job.error = text(j.error, "error");
  if (j.last !== undefined) {
    if (!isRecord(j.last)) throw invalid(RESPONSE, "last", "must be an object");
    job.last = {
      finishedAt: text(j.last.finishedAt, "last.finishedAt"),
      outcome: text(j.last.outcome, "last.outcome"),
    };
    if (j.last.reason !== undefined)
      job.last.reason = text(j.last.reason, "last.reason");
    if (j.last.error !== undefined)
      job.last.error = text(j.last.error, "last.error");
    if (j.last.result !== undefined) {
      const r = j.last.result;
      if (!isRecord(r) || !Array.isArray(r.accounts))
        throw invalid(RESPONSE, "last.result", "must be an account result");
      job.last.result = {
        skippedReason: text(r.skippedReason, "skippedReason"),
        accounts: r.accounts.map((a: unknown) => {
          if (!isRecord(a))
            throw invalid(RESPONSE, "accounts[]", "must be an object");
          return {
            account: text(a.account, "account"),
            error: text(a.error, "error"),
          };
        }),
      };
    }
  }
  return { job, runtimeError };
}
export async function fetchResetSchedule(
  client: ApiClient,
  signal?: AbortSignal,
): Promise<ResetScheduleState> {
  return decodeResetSchedule(
    await client.request(`${client.baseUrl}/account-pnl-reset-schedule`, {
      signal,
    }),
  );
}
export async function saveResetSchedule(
  client: ApiClient,
  input: ResetScheduleInput,
): Promise<ResetScheduleState> {
  return decodeResetSchedule(
    await client.request(`${client.baseUrl}/account-pnl-reset-schedule`, {
      method: "PUT",
      body: input,
    }),
  );
}
export async function recoverResetSchedule(
  client: ApiClient,
  revision: number,
  action: "retry" | "skip",
): Promise<ResetScheduleState> {
  return decodeResetSchedule(
    await client.request(
      `${client.baseUrl}/account-pnl-reset-schedule/recover`,
      { method: "POST", body: { revision, action } },
    ),
  );
}
export async function resetRealizedAccountPnl(
  client: ApiClient,
  code: string,
): Promise<void> {
  await client.request(
    `${client.baseUrl}/accounts/${encodeURIComponent(code)}/realized-pnl/reset`,
    { method: "POST" },
  );
}
