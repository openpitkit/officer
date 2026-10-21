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

const RFC_3339 =
  /^(?<year>\d{4})-(?<month>\d{2})-(?<day>\d{2})[Tt](?<hour>\d{2}):(?<minute>\d{2}):(?<second>\d{2})(?:\.\d+)?(?:[Zz]|(?<sign>[+-])(?<offsetHours>\d{2}):(?<offsetMinutes>\d{2}))$/;

/** Whether `value` is an RFC 3339 timestamp of a real time. */
export function isRfc3339Time(value: unknown): value is string {
  if (typeof value !== "string") {
    return false;
  }
  const fields = RFC_3339.exec(value)?.groups;
  const time = Date.parse(value);
  if (fields === undefined || Number.isNaN(time)) {
    return false;
  }
  const offset =
    fields.sign === undefined
      ? 0
      : (fields.sign === "-" ? -1 : 1) *
        (Number(fields.offsetHours) * 60 + Number(fields.offsetMinutes));
  // Date.parse rolls a day past the end of its month into the next one, so
  // the instant, read back at the value's own offset, must show the fields
  // the value wrote.
  const written = new Date(time + offset * 60_000);
  return (
    written.getUTCFullYear() === Number(fields.year) &&
    written.getUTCMonth() + 1 === Number(fields.month) &&
    written.getUTCDate() === Number(fields.day) &&
    written.getUTCHours() === Number(fields.hour) &&
    written.getUTCMinutes() === Number(fields.minute) &&
    written.getUTCSeconds() === Number(fields.second)
  );
}
