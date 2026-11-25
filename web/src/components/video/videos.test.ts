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

function captioned(id: string, language: string): string {
  return `${WATCH}${id}&cc_load_policy=1&cc_lang_pref=${language}`;
}

describe("videoUrl", () => {
  it.each<[VideoKey, string, string, string]>([
    ["prices", "7nM26GUgtes", "8iZq4OaV0TY", "ZW2b4hCLiXc"],
    ["size", "aXXWLLw3Jm0", "HmK7fCvACjE", "D_6FDDzgSK8"],
    ["rate", "BpQoS5UUEsU", "tILQ7UjSy7Q", "nFaBrgyjenY"],
    ["pnl", "kkcir-NpZBA", "XVVI18V3dqk", "nBQguXs8lic"],
    ["stop", "ateQ-i5k-o0", "WcZLKGHFFGQ", "PQrGg_cDYX0"],
    ["whyBalances", "na6qjWOZodc", "qOPaL-VOyAM", "vYG_beCi1Go"],
    ["balances", "6Dv9wQnFnRM", "BRz6M3O_3sU", "6NopWBreP7k"],
    ["groups", "_zRQy5L4Luw", "p885xVaJQkU", "GmLHCaiaKGo"],
  ])(
    "opens the %s guide in its en, ru and es cuts, else captioned English",
    (key, en, ru, es) => {
      expect(LOCALE_CODES.map((code) => videoUrl(key, code))).toEqual([
        `${WATCH}${en}`,
        `${WATCH}${ru}`,
        captioned(en, "zh-CN"),
        `${WATCH}${es}`,
        captioned(en, "pt-BR"),
      ]);
    },
  );

  it.each<[VideoKey, string]>([
    ["whyPrices", "tsv2ABKFdSk"],
    ["orderReview", "qE-WXp3Sz78"],
    ["testConnection", "2ke1H6TuN0E"],
    ["newTradingConnections", "d4NQ3fGXyww"],
    ["alpacaMcp", "y6_4DJy2tUo"],
    ["codexTrading", "pEUH5cbitBo"],
    ["cursorTrading", "eqd5iH2yaiA"],
  ])(
    "opens the English %s guide captioned in every other language",
    (key, en) => {
      expect(LOCALE_CODES.map((code) => videoUrl(key, code))).toEqual([
        `${WATCH}${en}`,
        captioned(en, "ru"),
        captioned(en, "zh-CN"),
        captioned(en, "es"),
        captioned(en, "pt-BR"),
      ]);
    },
  );

  it.each<[VideoKey, string]>([
    ["whereDataLives", "4q_1yB2SQqA"],
    ["canAgentBypass", "jxoP79byyGc"],
  ])("opens the English %s guide as it is in every language", (key, en) => {
    expect(LOCALE_CODES.map((code) => videoUrl(key, code))).toEqual(
      LOCALE_CODES.map(() => `${WATCH}${en}`),
    );
  });

  it.each<[VideoKey, string[]]>([
    [
      "intro",
      [
        "AILRFZbGxJs",
        "JxCHImpsmc4",
        "zMAwfEEA2oA",
        "-yPBdRiOjs0",
        "rQ2xRUP4mtQ",
      ],
    ],
    [
      "paperTradingFirst",
      [
        "LgDSHpEeHpo",
        "QpGpnPY63jM",
        "FBkilwQH90w",
        "u_cZfqSJBqE",
        "VvRa4EBL7jU",
      ],
    ],
  ])("opens the %s guide in every panel language's own cut", (key, ids) => {
    expect(LOCALE_CODES.map((code) => videoUrl(key, code))).toEqual(
      ids.map((id) => `${WATCH}${id}`),
    );
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
    expect(videoUrl("intro", "de")).toBe(`${WATCH}AILRFZbGxJs`);
    expect(videoUrl("mcp", "de")).toBe(`${WATCH}zq7a9xc0GD0&t=121s`);
  });
});
