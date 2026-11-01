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

import { videoUrl, type VideoKey } from "@/components/video/videos";
import { LOCALE_CODES } from "@/i18n/locales";

const WATCH = "https://www.youtube.com/watch?v=";

describe("videoUrl", () => {
  it.each<[VideoKey, string, string]>([
    ["prices", "7nM26GUgtes", "7nM26GUgtes"],
    ["size", "7-SWHs4UcGw", "pVrBefiSrpI"],
    ["rate", "zvvNHQ7HRYA", "ZewFwybR79E"],
    ["pnl", "IGXd2cSLKwM", "LFlWncmjnaM"],
    ["stop", "ateQ-i5k-o0", "WcZLKGHFFGQ"],
    ["whyBalances", "na6qjWOZodc", "na6qjWOZodc"],
    ["balances", "6Dv9wQnFnRM", "BRz6M3O_3sU"],
    ["groups", "Tv9vqqmw2Kg", "Tv9vqqmw2Kg"],
  ])("opens the whole %s guide without requesting captions", (key, en, ru) => {
    expect(LOCALE_CODES.map((code) => videoUrl(key, code))).toEqual([
      `${WATCH}${en}`,
      `${WATCH}${ru}`,
      `${WATCH}${en}`,
      `${WATCH}${en}`,
      `${WATCH}${en}`,
    ]);
  });

  it("opens the intro in every panel language's own cut", () => {
    expect(LOCALE_CODES.map((code) => videoUrl("intro", code))).toEqual([
      `${WATCH}v34IdYhH5so`,
      `${WATCH}F1ymuREbu4o`,
      `${WATCH}JxojlbYnGIE`,
      `${WATCH}Oq13uf44pEc`,
      `${WATCH}xgUOyBwjsgg`,
    ]);
  });

  it("opens the MCP chapter: own cut, else English with captions", () => {
    expect(LOCALE_CODES.map((code) => videoUrl("mcp", code))).toEqual([
      `${WATCH}zq7a9xc0GD0&t=121s`,
      `${WATCH}n_AWcXqedV0&t=136s`,
      `${WATCH}zq7a9xc0GD0&t=121s&cc_load_policy=1&cc_lang_pref=zh-CN`,
      `${WATCH}zq7a9xc0GD0&t=121s&cc_load_policy=1&cc_lang_pref=es`,
      `${WATCH}zq7a9xc0GD0&t=121s&cc_load_policy=1&cc_lang_pref=pt-BR`,
    ]);
  });

  it("opens the English cut as it is for a language with neither", () => {
    expect(videoUrl("intro", "de")).toBe(`${WATCH}v34IdYhH5so`);
    expect(videoUrl("mcp", "de")).toBe(`${WATCH}zq7a9xc0GD0&t=121s`);
  });
});
