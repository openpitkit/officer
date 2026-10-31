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

export function settingString(
  settings: Record<string, unknown> | undefined,
  key: string,
  fallback = "",
): string {
  const value = settings?.[key];
  if (typeof value === "string") {
    return value;
  }
  if (typeof value === "number" && Number.isFinite(value)) {
    return String(value);
  }
  return fallback;
}

export function isOptionalInt64(value?: string): boolean {
  const trimmed = value?.trim() ?? "";
  if (trimmed === "") {
    return true;
  }
  if (!/^[+-]?\d+$/u.test(trimmed)) {
    return false;
  }
  const parsed = BigInt(trimmed);
  return parsed >= -9223372036854775808n && parsed <= 9223372036854775807n;
}

export function isOptionalJSONInt64(value?: string): boolean {
  const trimmed = value?.trim() ?? "";
  if (trimmed === "") {
    return true;
  }
  if (!/^\+?\d+$/u.test(trimmed) || !isOptionalInt64(trimmed)) {
    return false;
  }
  return Number.isSafeInteger(Number(trimmed));
}

export function putIfFilled(
  target: Record<string, unknown>,
  key: string,
  value: string | undefined,
) {
  const trimmed = value?.trim() ?? "";
  if (trimmed !== "") {
    target[key] = trimmed;
  }
}

export function putNumberIfFilled(
  target: Record<string, unknown>,
  key: string,
  value: string | undefined,
) {
  const trimmed = value?.trim() ?? "";
  if (trimmed !== "") {
    const numeric = Number(trimmed);
    if (Number.isFinite(numeric)) {
      target[key] = numeric;
    }
  }
}
