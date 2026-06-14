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

const COOKIE_MAX_AGE_SECONDS = 60 * 60 * 24 * 365;

function readCookie(key: string): string | null {
  if (typeof document === "undefined") {
    return null;
  }
  const encodedKey = encodeURIComponent(key);
  for (const part of document.cookie.split(";")) {
    const trimmed = part.trim();
    if (!trimmed.startsWith(`${encodedKey}=`)) {
      continue;
    }
    return decodeURIComponent(trimmed.slice(encodedKey.length + 1));
  }
  return null;
}

function writeCookie(key: string, value: string): void {
  if (typeof document === "undefined") {
    return;
  }
  document.cookie = [
    `${encodeURIComponent(key)}=${encodeURIComponent(value)}`,
    `Max-Age=${COOKIE_MAX_AGE_SECONDS}`,
    "Path=/",
    "SameSite=Lax",
  ].join("; ");
}

export function readStoredPreference(key: string): string | null {
  if (typeof window !== "undefined") {
    try {
      const stored = window.localStorage.getItem(key);
      if (stored !== null) {
        return stored;
      }
    } catch {
      // Fall through to the cookie mirror.
    }
  }
  return readCookie(key);
}

export function writeStoredPreference(key: string, value: string): void {
  if (typeof window !== "undefined") {
    try {
      window.localStorage.setItem(key, value);
    } catch {
      // The cookie mirror still gives service-restart and port-change fallback.
    }
  }
  writeCookie(key, value);
}
