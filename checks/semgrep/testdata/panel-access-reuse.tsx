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

import { PasswordPairFields } from "@/components/PasswordPairFields";
import { RevealButton } from "@/components/RevealButton";
import { isRfc3339Time } from "@/framework/api/rfc3339";
import { errorResponse } from "@/test/apiClient";

function copiedFailure(status, code, headers) {
  // ruleid: shared-panel-error-response
  const res = jsonResponse({ error: { code, message: code } }, status);
  for (const [name, value] of Object.entries(headers)) {
    res.headers.set(name, value);
  }
  return res;
}

function copiedTime(written, fields) {
  // ruleid: shared-panel-rfc3339
  return written.getUTCFullYear() === Number(fields.year) &&
    written.getUTCMonth() + 1 === Number(fields.month) &&
    written.getUTCDate() === Number(fields.day) &&
    written.getUTCHours() === Number(fields.hour) &&
    written.getUTCMinutes() === Number(fields.minute) &&
    written.getUTCSeconds() === Number(fields.second);
}

// ruleid: shared-panel-password-pair
const password = <TextField autoComplete="new-password" />;
// ruleid: shared-panel-password-pair
const repeat = <SecretInput autoComplete="new-password" />;
// ruleid: shared-panel-reveal-button
const label = shown ? t("actions.hide") : t("actions.show");

// ok: shared-panel-error-response
const failure = errorResponse(401, "unauthorized", {}, "");
// ok: shared-panel-error-response
const success = jsonResponse({ authenticated: true }, 200);
// ok: shared-panel-rfc3339
const time = isRfc3339Time(value);
// ok: shared-panel-rfc3339
const year = written.getUTCFullYear();
// ok: shared-panel-password-pair
const pair = <PasswordPairFields password={passwordProps} repeat={repeatProps} disabled={false} />;
// ok: shared-panel-password-pair
const current = <SecretInput autoComplete="current-password" />;
// ok: shared-panel-password-pair
const recovery = <TextField autoComplete="one-time-code" />;
// ok: shared-panel-reveal-button
const reveal = <RevealButton shown={shown} onToggle={toggle} />;
// ok: shared-panel-reveal-button
const unrelated = shown ? t("actions.cancel") : t("actions.save");
