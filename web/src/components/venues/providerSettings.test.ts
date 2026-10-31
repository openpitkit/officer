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

import { describe, expect, it } from "vitest";

import {
  isOptionalInt64,
  isOptionalJSONInt64,
  putIfFilled,
  putNumberIfFilled,
  settingString,
} from "./providerSettings";

describe("provider settings", () => {
  it("reads only strings and finite numbers from settings", () => {
    const settings = {
      host: "localhost",
      port: 4002,
      flag: true,
      nan: NaN,
      inf: Infinity,
      object: {},
    };
    expect(settingString(settings, "host")).toBe("localhost");
    expect(settingString(settings, "port")).toBe("4002");
    for (const key of ["missing", "flag", "nan", "inf", "object"]) {
      expect(settingString(settings, key)).toBe("");
      expect(settingString(settings, key, "explicit")).toBe("explicit");
    }
    expect(settingString(undefined, "host")).toBe("");
  });

  it.each([
    undefined,
    "",
    "  ",
    "0",
    "+1",
    "-1",
    "-9223372036854775808",
    "9223372036854775807",
  ])("accepts optional int64 %s", (value) => {
    expect(isOptionalInt64(value)).toBe(true);
  });

  it.each([
    "9223372036854775808",
    "-9223372036854775809",
    "1.5",
    "1e2",
    "NaN",
    "abc",
  ])("rejects invalid int64 %s", (value) => {
    expect(isOptionalInt64(value)).toBe(false);
  });

  it.each([undefined, "", " ", "0", "+7", " 42 ", "9007199254740991"])(
    "accepts optional JSON int64 %s",
    (value) => {
      expect(isOptionalJSONInt64(value)).toBe(true);
    },
  );

  it.each([
    "-1",
    "1.2",
    "1e2",
    "9007199254740992",
    "9223372036854775807",
    "9223372036854775808",
    "abc",
  ])("rejects unsafe or negative JSON int64 %s", (value) => {
    expect(isOptionalJSONInt64(value)).toBe(false);
  });

  it("writes filled trimmed values and finite numbers, leaving existing keys for blanks", () => {
    const target: Record<string, unknown> = { host: "stored", port: 4002 };
    putIfFilled(target, "host", " ");
    putIfFilled(target, "absent", undefined);
    putNumberIfFilled(target, "port", "");
    putNumberIfFilled(target, "absent", undefined);
    putNumberIfFilled(target, "invalid", "abc");
    putNumberIfFilled(target, "infinite", "Infinity");
    expect(target).toEqual({ host: "stored", port: 4002 });
    putIfFilled(target, "host", " localhost ");
    putNumberIfFilled(target, "port", " 7497 ");
    putNumberIfFilled(target, "clientId", "0");
    expect(target).toEqual({ host: "localhost", port: 7497, clientId: 0 });
  });
});
